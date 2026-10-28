package protocol

import "strconv"

// Snapshots use constructed objects; token increments append into the charged, reusable event buffer.
func (s *chatToNative) deltaStart(name, suffix string, room int) error {
	var err error
	s.out, err = grow(&s.aggBase, s.out, 512+2*len(name)+2*len(suffix)+room, 2) // framing and worst-width indexes
	if err != nil {
		return err
	}
	s.out = append(append(append(s.out, "event: "...), name...), suffix...)
	s.out = append(s.out, "\ndata: {\"type\":\""...)
	s.out = append(append(append(s.out, name...), suffix...), '"')
	if s.client == Responses {
		s.out = strconv.AppendInt(append(s.out, `,"sequence_number":`...), s.seq, 10)
		s.seq++
	}
	return nil
}

func (s *chatToNative) messageIncrement(i int, kind, member string) error {
	it := &s.items[i]
	if err := s.deltaStart("content_block_delta", "", len(it.text)-it.sent+len(kind)+len(member)); err != nil {
		return err
	}
	s.out = strconv.AppendInt(append(s.out, `,"index":`...), int64(i), 10)
	s.out = append(append(append(s.out, `,"delta":{"type":"`...), kind...), `","`...)
	s.out = append(append(s.out, member...), `":"`...)
	s.out = append(append(s.out, it.text[it.sent:]...), `"}}`+"\n\n"...)
	it.sent = len(it.text)
	return nil
}

func (s *chatToNative) responseIncrement(i int, name, index string) error {
	it := &s.items[i]
	if err := s.deltaStart(name, ".delta", len(it.text)-it.sent+quoted(len(it.responseID))+len(index)); err != nil {
		return err
	}
	s.out = strconv.AppendInt(append(s.out, `,"output_index":`...), int64(i), 10)
	s.out = appendQuoted(append(s.out, `,"item_id":`...), it.responseID)
	if index != "" {
		s.out = append(append(append(s.out, `,"`...), index...), `":0`...)
	}
	s.out = append(append(append(s.out, `,"delta":"`...), it.text[it.sent:]...), `"}`+"\n\n"...)
	it.sent = len(it.text)
	return nil
}
