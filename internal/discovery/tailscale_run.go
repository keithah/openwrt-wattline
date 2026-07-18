package discovery

import (
	"errors"
	"io"
)

const maxCommandOutput = 1 << 20

var ErrCommandOutputTooLarge = errors.New("command output exceeds 1 MiB")

func readBoundedOutput(reader io.Reader) ([]byte, bool, error) {
	output, err := io.ReadAll(&io.LimitedReader{R: reader, N: maxCommandOutput + 1})
	if len(output) > maxCommandOutput {
		return output[:maxCommandOutput], true, err
	}
	return output, false, err
}
