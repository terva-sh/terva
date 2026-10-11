package tui

import "bufio"

func peekBufferedInput(reader *bufio.Reader, ready func() (bool, error)) (byte, bool, error) {
	if reader.Buffered() == 0 {
		ok, err := ready()
		if !ok || err != nil {
			return 0, false, err
		}
	}
	b, err := reader.ReadByte()
	return b, err == nil, err
}
