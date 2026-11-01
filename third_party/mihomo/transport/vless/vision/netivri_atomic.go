package vision

// lastCommand is the last padding command read from the peer; safe to call
// from a goroutine other than the one that reads.
func (vc *Conn) lastCommand() byte {
	return byte(vc.readLastCommand.Load())
}
