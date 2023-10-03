package lsp

func removeFromSlice(s []Message, seq int) []Message {
	for i, m := range s {
		if m.SeqNum == seq {
			return append(s[:i], s[i+1:]...)
		}
	}
	return s
}

func removeFromSlice_CAck(s []Message, seq int) []Message {
	for i, m := range s {
		if m.SeqNum == seq {
			return s[i+1:]
		}
	}
	return s
}
