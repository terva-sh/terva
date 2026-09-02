package extconn

// InboundDrops reports the messages lost to a full inbound queue this
// process run (chat.DropCounter). The queue is long-lived across
// redials, and so is the count.
func (c *Conn) InboundDrops() uint64 { return c.drops.Load() }
