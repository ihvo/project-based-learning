package peer

import (
	"fmt"

	"github.com/ihvo/peer-pressure/bencode"
)

// EncodeUploadOnly creates the bencoded payload for a BEP 21 upload_only message.
func EncodeUploadOnly(uploadOnly bool) []byte {
	val := bencode.Int(0)
	if uploadOnly {
		val = 1
	}
	return bencode.Encode(bencode.Dict{"upload_only": val})
}

// DecodeUploadOnly parses a BEP 21 upload_only message payload.
// Handles both dict form {"upload_only": N} and bare integer form.
func DecodeUploadOnly(payload []byte) (bool, error) {
	if len(payload) == 0 {
		return false, fmt.Errorf("empty upload_only payload")
	}

	val, err := bencode.Decode(payload)
	if err != nil {
		return false, fmt.Errorf("decode upload_only: %w", err)
	}

	switch v := val.(type) {
	case bencode.Dict:
		if uoVal, ok := v["upload_only"]; ok {
			if n, ok := uoVal.(bencode.Int); ok {
				return n != 0, nil
			}
		}
		return false, nil
	case bencode.Int:
		return v != 0, nil
	default:
		return false, fmt.Errorf("unexpected upload_only type: %T", val)
	}
}

// NewUploadOnlyMsg creates a full extended message for BEP 21 upload_only.
func NewUploadOnlyMsg(subID uint8, uploadOnly bool) *Message {
	return NewExtMessage(subID, EncodeUploadOnly(uploadOnly))
}
