package services

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/nacl/box"
)

// NodeKeypair 是本节点的长期身份密钥对.
//
// 设计意图:
//   - 取代全局 SyncSecret. 每节点生成自己的 X25519 keypair,
//     私钥永不出本机, 公钥手动交换 (类 SSH known_hosts 模型).
//   - 加密 payload 用 nacl/box (curve25519 + xsalsa20 + poly1305),
//     A → B: box.Seal(payload, nonce, B 的公钥, A 的私钥)
//     B 收到: box.Open(ciphertext, nonce, A 的公钥, B 的私钥)
//   - 单节点泄漏只影响该节点的私钥, 其他节点撤销该公钥即可立即隔离.
type NodeKeypair struct {
	PublicKey  [32]byte // X25519 公钥
	privateKey [32]byte // X25519 私钥, 不导出
}

const (
	keypairDir      = "data/peer_keys"
	publicKeyFile   = "x25519.pub"
	privateKeyFile  = "x25519.priv"
	fingerprintSize = 8 // 8 字节 / 12 字符 base64, 足够防碰撞且便于人眼比对
)

// NodeKeypairService 通过 dig 注入. 启动时:
//   - 优先从 data/peer_keys/ 加载已有密钥对
//   - 否则生成新的并落盘 (私钥权限 0o600)
type NodeKeypairService struct {
	once sync.Once
	kp   *NodeKeypair
	err  error
}

// NewNodeKeypairService 构造函数, 不在此处加载, 第一次 Get() 时懒加载.
func NewNodeKeypairService() *NodeKeypairService {
	return &NodeKeypairService{}
}

// Get 返回本节点密钥对, 首次调用时执行加载或生成.
func (s *NodeKeypairService) Get() (*NodeKeypair, error) {
	s.once.Do(func() {
		s.kp, s.err = loadOrCreate()
	})
	return s.kp, s.err
}

// MustGet 用于已知初始化成功的场景, 失败 panic. 不要在请求路径调用.
func (s *NodeKeypairService) MustGet() *NodeKeypair {
	kp, err := s.Get()
	if err != nil {
		panic(fmt.Errorf("node keypair unavailable: %w", err))
	}
	return kp
}

// PublicKeyBase64 给 /api/version 端点和 UI 使用. base64 标准编码, 44 字符.
func (s *NodeKeypairService) PublicKeyBase64() string {
	kp, err := s.Get()
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(kp.PublicKey[:])
}

// Fingerprint 返回公钥的短指纹 (前 8 字节 SHA256 → base64),
// 用户在 UI 上肉眼核对两端是否相同时用这个值.
//
// 标准 SSH key fingerprint 也是这种思路, 只是它用 SHA256 全长.
// 我们用短版避免 UI 占地, 8 字节碰撞概率 < 2^-64.
func (s *NodeKeypairService) Fingerprint() string {
	kp, err := s.Get()
	if err != nil {
		return ""
	}
	h := sha256.Sum256(kp.PublicKey[:])
	return base64.RawStdEncoding.EncodeToString(h[:fingerprintSize])
}

// EncryptFor 用 nacl/box 把明文加密给指定对端 (用对端公钥).
// 返回值 = nonce(24) || ciphertext, 整体再 base64 标准编码.
func (s *NodeKeypairService) EncryptFor(plaintext []byte, recipientPublicKey *[32]byte) (string, error) {
	kp, err := s.Get()
	if err != nil {
		return "", err
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	sealed := box.Seal(nonce[:], plaintext, &nonce, recipientPublicKey, &kp.privateKey)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// DecryptFrom 用 nacl/box 解密一个来自指定对端的密文 (用对端公钥 + 本机私钥).
func (s *NodeKeypairService) DecryptFrom(ciphertextB64 string, senderPublicKey *[32]byte) ([]byte, error) {
	kp, err := s.Get()
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(raw) < 24 {
		return nil, fmt.Errorf("ciphertext too short")
	}
	var nonce [24]byte
	copy(nonce[:], raw[:24])
	plaintext, ok := box.Open(nil, raw[24:], &nonce, senderPublicKey, &kp.privateKey)
	if !ok {
		return nil, fmt.Errorf("box.Open failed (wrong key or tampered)")
	}
	return plaintext, nil
}

// loadOrCreate 加载已有密钥, 找不到就生成新对并落盘.
func loadOrCreate() (*NodeKeypair, error) {
	pubPath := filepath.Join(keypairDir, publicKeyFile)
	privPath := filepath.Join(keypairDir, privateKeyFile)

	// 已存在? 加载.
	if pubBytes, err := os.ReadFile(pubPath); err == nil {
		privBytes, err := os.ReadFile(privPath)
		if err != nil {
			return nil, fmt.Errorf("private key missing but public exists: %w", err)
		}
		if len(pubBytes) != 32 || len(privBytes) != 32 {
			return nil, fmt.Errorf("key file corrupted (wrong size)")
		}
		kp := &NodeKeypair{}
		copy(kp.PublicKey[:], pubBytes)
		copy(kp.privateKey[:], privBytes)
		return kp, nil
	}

	// 不存在 — 生成新的
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	if err := os.MkdirAll(keypairDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", keypairDir, err)
	}
	// 私钥严格权限
	if err := os.WriteFile(privPath, priv[:], 0o600); err != nil {
		return nil, fmt.Errorf("write priv: %w", err)
	}
	if err := os.WriteFile(pubPath, pub[:], 0o644); err != nil {
		return nil, fmt.Errorf("write pub: %w", err)
	}
	return &NodeKeypair{PublicKey: *pub, privateKey: *priv}, nil
}

// DecodePublicKeyBase64 解析 UI/握手传来的 base64 公钥字符串.
func DecodePublicKeyBase64(s string) (*[32]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("base64: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("public key must be 32 bytes, got %d", len(raw))
	}
	var out [32]byte
	copy(out[:], raw)
	return &out, nil
}

// FingerprintOf 接受一个 base64 公钥, 返回该公钥的短指纹. UI 校验用.
func FingerprintOf(publicKeyBase64 string) string {
	pk, err := DecodePublicKeyBase64(publicKeyBase64)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(pk[:])
	return base64.RawStdEncoding.EncodeToString(h[:fingerprintSize])
}
