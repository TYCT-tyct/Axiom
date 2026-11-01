package protocol

// blocksSize is the bytes blocks can write for a message, used to reserve the parts buffer before it grows.
func blocksSize(msg canonMessage) int {
	n := 256
	for _, p := range msg.Parts {
		n += len(p.Text) + len(p.CacheControl)
		if p.ImageURL != nil {
			n += len(p.ImageURL.URL)
		}
		if p.File != nil && p.File.FileData != nil {
			n += len(*p.File.FileData)
		}
	}
	return n
}
