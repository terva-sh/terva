package connlocal

// InboundDrops reports the messages lost to a full inbound queue this
// process run (chat.DropCounter).
func (c *Conn) InboundDrops() uint64 { return c.drops.Load() }
