package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"

	"golang.org/x/crypto/argon2"
)

const (
	saltLen   = 16
	nonceLen  = 12
	keyLen    = 32
	argonTime = 3
	argonMem  = 64 * 1024 // 64 MiB
	argonPar  = 4
)

// EncryptedBox 是一次 Seal 的全部输出，由 codec 负责落盘 / 还原。
type EncryptedBox struct {
	Salt       [saltLen]byte
	Nonce      [nonceLen]byte
	AAD        []byte
	Ciphertext []byte // 含 GCM tag
}

// DeriveKey 用 Argon2id 从 password 派生 32 字节对称密钥。
func DeriveKey(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, argonTime, argonMem, argonPar, keyLen)
}

// Seal 把 plaintext 用 password 加密为 EncryptedBox。
func Seal(plaintext []byte, password string) (*EncryptedBox, error) {
	box := &EncryptedBox{}
	if _, err := rand.Read(box.Salt[:]); err != nil {
		return nil, fmt.Errorf("salt: %w", err)
	}
	if _, err := rand.Read(box.Nonce[:]); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}

	key := DeriveKey(password, box.Salt[:])
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}

	box.AAD = []byte{'A', 'C', 'B', '1', 0x01} // matches header magic+version
	box.Ciphertext = aead.Seal(nil, box.Nonce[:], plaintext, box.AAD)
	return box, nil
}

// Open 验证 AAD/tag 并解出 plaintext。密码错或 ciphertext 篡改返回错误。
func Open(box *EncryptedBox, password string) ([]byte, error) {
	key := DeriveKey(password, box.Salt[:])
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	pt, err := aead.Open(nil, box.Nonce[:], box.Ciphertext, box.AAD)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return pt, nil
}
