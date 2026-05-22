package backup

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	magicLen        = 4
	headerFixedLen  = 8 // magic(4) + ver(1) + kdf(1) + cipher(1) + reserved(1)
	currentVersion  = 0x01
	kdfArgon2id     = 0x01
	cipherAESGCM256 = 0x01
)

var magic = [4]byte{'A', 'C', 'B', '1'}

// EncodeContainer 用 Seal 加密 plaintext 并组装成 .acb 字节流。
func EncodeContainer(plaintext []byte, password string) ([]byte, error) {
	box, err := Seal(plaintext, password)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, headerFixedLen+saltLen+nonceLen+4+len(box.AAD)+len(box.Ciphertext))
	out = append(out, magic[:]...)
	out = append(out, currentVersion, kdfArgon2id, cipherAESGCM256, 0x00)
	out = append(out, box.Salt[:]...)
	out = append(out, box.Nonce[:]...)

	aadLen := make([]byte, 4)
	binary.BigEndian.PutUint32(aadLen, uint32(len(box.AAD)))
	out = append(out, aadLen...)
	out = append(out, box.AAD...)
	out = append(out, box.Ciphertext...)
	return out, nil
}

// DecodeContainer 解析 .acb 字节流并解密。
func DecodeContainer(blob []byte, password string) ([]byte, error) {
	if len(blob) < headerFixedLen+saltLen+nonceLen+4 {
		return nil, errors.New("acb: truncated header")
	}
	if string(blob[0:magicLen]) != string(magic[:]) {
		return nil, errors.New("acb: bad magic")
	}
	ver := blob[4]
	if ver != currentVersion {
		return nil, fmt.Errorf("acb: unsupported container version %d", ver)
	}
	if blob[5] != kdfArgon2id {
		return nil, fmt.Errorf("acb: unsupported kdf id %d", blob[5])
	}
	if blob[6] != cipherAESGCM256 {
		return nil, fmt.Errorf("acb: unsupported cipher id %d", blob[6])
	}
	off := headerFixedLen

	box := &EncryptedBox{}
	copy(box.Salt[:], blob[off:off+saltLen])
	off += saltLen
	copy(box.Nonce[:], blob[off:off+nonceLen])
	off += nonceLen

	if len(blob) < off+4 {
		return nil, errors.New("acb: truncated aad_len")
	}
	aadLen := int(binary.BigEndian.Uint32(blob[off : off+4]))
	off += 4
	if len(blob) < off+aadLen {
		return nil, errors.New("acb: truncated aad")
	}
	box.AAD = blob[off : off+aadLen]
	off += aadLen
	box.Ciphertext = blob[off:]

	return Open(box, password)
}
