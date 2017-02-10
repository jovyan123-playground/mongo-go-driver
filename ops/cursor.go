package ops

import (
	"github.com/10gen/mongo-go-driver/core"
	"github.com/10gen/mongo-go-driver/core/msg"
	"gopkg.in/mgo.v2/bson"
)

// NewCursor creates a new cursor from the given cursor result.
func NewCursor(cursorResult CursorResult, batchSize int32, connection core.Connection) (Cursor, error) {
	namespace := cursorResult.Namespace()
	if err := namespace.validate(); err != nil {
		return nil, err
	}

	return &cursorImpl{
		namespace:    cursorResult.Namespace(),
		batchSize:    batchSize,
		current:      0,
		currentBatch: cursorResult.InitialBatch(),
		cursorId:     cursorResult.CursorId(),
		connection:   connection,
	}, nil
}

// Cursor instances iterate a stream of documents. Each document is decoded into the result according to the rules of
// the bson package.  A typical usage of the Cursor interface would be:
//
//      cursor := ...    // get a cursor from some operation
//      var doc bson.D
//      for cursor.Next(&doc) {
//              fmt.Println(doc)
//      err := cursor.Close()
type Cursor interface {
	// Get the next result from the cursor.
	// Returns true if there were no errors and there is a next result.
	Next(result interface{}) bool

	// Returns the error status of the cursor
	Err() error

	// Close the cursor.  Ordinarily this is a no-op as the server closes the cursor when it is exhausted.
	// Returns the error status of this cursor so that clients do not have to call Err() separately
	Close() error
}

type cursorImpl struct {
	namespace    Namespace
	batchSize    int32
	current      int
	currentBatch []bson.Raw
	cursorId     int64
	err          error
	connection   core.Connection // TODO: missing abstraction.  Shouldn't require a connection here, but just a way to acquire and release one
}

func (c *cursorImpl) Next(result interface{}) bool {
	found := c.getNextFromCurrentBatch(result)
	if found {
		return true
	}
	if c.err != nil {
		return false
	}

	c.getMore()
	if c.err != nil {
		return false
	}

	return c.getNextFromCurrentBatch(result)
}

func (c *cursorImpl) Err() error {
	return c.err
}

func (c *cursorImpl) Close() error {
	c.currentBatch = nil

	if c.cursorId == 0 {
		return c.err
	}

	killCursorsCommand := struct {
		Collection string  `bson:"killCursors"`
		Cursors    []int64 `bson:"cursors"`
	}{
		Collection: c.namespace.Collection,
		Cursors:    []int64{c.cursorId},
	}

	killCursorsRequest := msg.NewCommand(
		msg.NextRequestID(),
		c.namespace.DB,
		false,
		killCursorsCommand,
	)

	err := core.ExecuteCommand(c.connection, killCursorsRequest, &bson.D{})
	if err == nil {
		c.cursorId = 0
	} else if c.err == nil {
		c.err = err
	}

	return c.err
}

func (c *cursorImpl) getNextFromCurrentBatch(result interface{}) bool {
	if c.current < len(c.currentBatch) {
		err := bson.Unmarshal(c.currentBatch[c.current].Data, result)
		if err != nil {
			c.err = err
			return false
		}
		c.current++
		return true
	}
	return false
}

func (c *cursorImpl) getMore() {
	c.currentBatch = nil
	c.current = 0

	if c.cursorId == 0 {
		return
	}

	getMoreCommand := struct {
		CursorId   int64  `bson:"getMore"`
		Collection string `bson:"collection"`
		BatchSize  int32  `bson:"batchSize,omitempty"`
	}{
		CursorId:   c.cursorId,
		Collection: c.namespace.Collection,
	}
	if c.batchSize != 0 {
		getMoreCommand.BatchSize = c.batchSize
	}
	getMoreRequest := msg.NewCommand(
		msg.NextRequestID(),
		c.namespace.DB,
		false,
		getMoreCommand,
	)

	var response struct {
		OK     bool `bson:"ok"`
		Cursor struct {
			       NextBatch []bson.Raw `bson:"nextBatch"`
			       NS        string     `bson:"ns"`
			       ID        int64      `bson:"id"`
		       } `bson:"cursor"`
	}

	err := core.ExecuteCommand(c.connection, getMoreRequest, &response)
	if err != nil {
		c.err = err
		return
	}

	c.cursorId = response.Cursor.ID
	c.currentBatch = response.Cursor.NextBatch
}