package services

import (
	"os"
	"path/filepath"
	"testing"
)

// withTempKeypairDir 临时切换 working directory 让 loadOrCreate 落盘到 tmp.
func withTempKeypairDir(t *testing.T) (cleanup func()) {
	t.Helper()
	tmp := t.TempDir()
	orig, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Chdir(orig) }
}

func TestNodeKeypair_RoundTrip(t *testing.T) {
	defer withTempKeypairDir(t)()

	alice := NewNodeKeypairService()
	bob := NewNodeKeypairService()

	// 不同节点产生不同密钥 — 切回原 dir 让 bob 新建一组
	// 实际上他们指向同一个 keypair_dir, 所以先让 alice 完成, 然后清理重来
	kpA, err := alice.Get()
	if err != nil {
		t.Fatalf("alice keypair: %v", err)
	}

	// 删除文件让 bob 生成新的
	_ = os.RemoveAll(keypairDir)

	kpB, err := bob.Get()
	if err != nil {
		t.Fatalf("bob keypair: %v", err)
	}

	if kpA.PublicKey == kpB.PublicKey {
		t.Fatal("alice and bob got same public key (should be random)")
	}

	// 模拟 alice → bob 加密
	alicePubB64 := alice.PublicKeyBase64()
	bobPubB64 := bob.PublicKeyBase64()

	bobPubKey, err := DecodePublicKeyBase64(bobPubB64)
	if err != nil {
		t.Fatalf("decode bob pub: %v", err)
	}
	alicePubKey, err := DecodePublicKeyBase64(alicePubB64)
	if err != nil {
		t.Fatalf("decode alice pub: %v", err)
	}

	plaintext := []byte(`{"hello":"mesh"}`)
	ciphertext, err := alice.EncryptFor(plaintext, bobPubKey)
	if err != nil {
		t.Fatalf("alice encrypt: %v", err)
	}
	if ciphertext == "" {
		t.Fatal("empty ciphertext")
	}

	decoded, err := bob.DecryptFrom(ciphertext, alicePubKey)
	if err != nil {
		t.Fatalf("bob decrypt: %v", err)
	}
	if string(decoded) != string(plaintext) {
		t.Errorf("roundtrip mismatch: got %q want %q", decoded, plaintext)
	}
}

func TestNodeKeypair_TamperDetected(t *testing.T) {
	defer withTempKeypairDir(t)()

	alice := NewNodeKeypairService()
	_, _ = alice.Get()
	_ = os.RemoveAll(keypairDir)
	bob := NewNodeKeypairService()
	_, _ = bob.Get()

	bobPub, _ := DecodePublicKeyBase64(bob.PublicKeyBase64())
	alicePub, _ := DecodePublicKeyBase64(alice.PublicKeyBase64())

	ct, _ := alice.EncryptFor([]byte("secret"), bobPub)
	// 把密文末尾 1 字符改一下 (篡改)
	tampered := ct[:len(ct)-1] + "A"

	if _, err := bob.DecryptFrom(tampered, alicePub); err == nil {
		t.Fatal("tampered ciphertext should fail box.Open")
	}
}

func TestNodeKeypair_WrongSenderRejected(t *testing.T) {
	defer withTempKeypairDir(t)()

	alice := NewNodeKeypairService()
	_, _ = alice.Get()
	_ = os.RemoveAll(keypairDir)
	bob := NewNodeKeypairService()
	_, _ = bob.Get()
	_ = os.RemoveAll(keypairDir)
	eve := NewNodeKeypairService()
	_, _ = eve.Get()

	bobPub, _ := DecodePublicKeyBase64(bob.PublicKeyBase64())
	evePub, _ := DecodePublicKeyBase64(eve.PublicKeyBase64())

	// alice → bob 加密
	ct, _ := alice.EncryptFor([]byte("for bob"), bobPub)
	// bob 用 eve 的公钥尝试解密 → 必须失败 (公钥/私钥配对错误)
	if _, err := bob.DecryptFrom(ct, evePub); err == nil {
		t.Fatal("decrypting with wrong sender pub should fail")
	}
}

func TestFingerprint_Stable(t *testing.T) {
	defer withTempKeypairDir(t)()
	svc := NewNodeKeypairService()
	fp1 := svc.Fingerprint()
	fp2 := svc.Fingerprint()
	if fp1 != fp2 {
		t.Errorf("fingerprint not stable: %s vs %s", fp1, fp2)
	}
	if fp1 == "" {
		t.Fatal("empty fingerprint")
	}
	// FingerprintOf 应跟 service 自己的一致
	if got := FingerprintOf(svc.PublicKeyBase64()); got != fp1 {
		t.Errorf("FingerprintOf mismatch: %s vs %s", got, fp1)
	}
}

func TestKeypair_PersistedAcrossReloads(t *testing.T) {
	defer withTempKeypairDir(t)()
	svc1 := NewNodeKeypairService()
	pub1 := svc1.PublicKeyBase64()

	// 第二次实例化, 应加载已有密钥
	svc2 := NewNodeKeypairService()
	pub2 := svc2.PublicKeyBase64()

	if pub1 != pub2 {
		t.Errorf("keypair not persisted: %s vs %s", pub1, pub2)
	}

	// 确认文件权限
	info, err := os.Stat(filepath.Join(keypairDir, privateKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("private key file perm = %o, want 0600", info.Mode().Perm())
	}
}
