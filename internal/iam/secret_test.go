package iam

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

// Пароль по-прежнему проверяется: очередь хешей не меняет результата.
func TestPasswordRoundTrip(t *testing.T) {
	p := ArgonParams{Time: 1, Memory: 1024, Threads: 1, KeyLen: 32}
	hash, err := HashPassword("parol12345", p)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPassword("parol12345", hash); err != nil {
		t.Fatalf("верный пароль не прошёл: %v", err)
	}
	if err := VerifyPassword("parol12346", hash); err != ErrPasswordMismatch {
		t.Fatalf("неверный пароль: %v", err)
	}
}

// Одновременные входы не считают больше cap(argonSlots) хешей разом: каждый
// берёт 64 МБ, и 15 входов учителей разом занимали почти весь лимит памяти
// контейнера API.
func TestConcurrentHashesAreLimited(t *testing.T) {
	var running, peak atomic.Int32
	argonKey = func(password, salt []byte, time_, memory uint32, threads uint8, keyLen uint32) []byte {
		now := running.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		running.Add(-1)
		return argon2.IDKey(password, salt, 1, 8, 1, keyLen)
	}
	t.Cleanup(func() { argonKey = argon2.IDKey })

	p := ArgonParams{Time: 3, Memory: 65536, Threads: 2, KeyLen: 32}
	var wg sync.WaitGroup
	for i := 0; i < 26; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := HashPassword("parol12345", p); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := int(peak.Load()); got < 1 || got > cap(argonSlots) {
		t.Fatalf("одновременных хешей: %d, предел %d", got, cap(argonSlots))
	}
}
