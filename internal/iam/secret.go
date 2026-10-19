package iam

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ---------------------------------------------------------------------------
// Пароли
//
// argon2id, а не bcrypt: bcrypt режет пароль на 72 байтах и не настраивается
// по памяти. Формат строки самодостаточен — параметры лежат рядом с хешем,
// поэтому их можно поднять позже, не ломая старые записи.
//
//	$argon2id$v=19$m=65536,t=3,p=2$<salt b64>$<hash b64>
// ---------------------------------------------------------------------------

// ErrPasswordMismatch — пароль не подошёл.
var ErrPasswordMismatch = errors.New("iam: пароль не совпадает")

// argonSlots — сколько хешей argon2id считается одновременно. Каждый
// занимает Memory килобайт (64 МБ при m=65536): 15 одновременных входов
// заняли около 960 МБ при лимите контейнера API в 1 ГБ (замер 08.10.2026),
// и API падал бы по памяти, например когда учителя входят разом на
// семинаре. Остальные ждут очереди — это доли секунды.
var argonSlots = make(chan struct{}, 4)

// argonKey — расчёт ключа; тест подменяет его, чтобы считать одновременные.
var argonKey = argon2.IDKey

func idKey(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte {
	argonSlots <- struct{}{}
	defer func() { <-argonSlots }()
	return argonKey(password, salt, time, memory, threads, keyLen)
}

// ArgonParams — стоимость хеширования. Берётся из конфигурации.
type ArgonParams struct {
	Time    uint32
	Memory  uint32 // килобайты
	Threads uint8
	KeyLen  uint32
}

// HashPassword считает argon2id-хеш со свежей солью.
func HashPassword(password string, p ArgonParams) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("iam: не удалось получить соль: %w", err)
	}
	key := idKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword сверяет пароль с хешем.
//
// Параметры берутся из самой строки хеша, а не из конфигурации: иначе после
// повышения стоимости перестали бы проходить все старые пароли.
func VerifyPassword(password, encoded string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return fmt.Errorf("iam: неподдерживаемый формат хеша пароля")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return fmt.Errorf("iam: не удалось разобрать версию хеша: %w", err)
	}
	if version != argon2.Version {
		return fmt.Errorf("iam: несовместимая версия argon2: %d", version)
	}

	var memory, timeCost uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeCost, &threads); err != nil {
		return fmt.Errorf("iam: не удалось разобрать параметры хеша: %w", err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return fmt.Errorf("iam: не удалось разобрать соль: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return fmt.Errorf("iam: не удалось разобрать хеш: %w", err)
	}

	got := idKey([]byte(password), salt, timeCost, memory, threads, uint32(len(want)))
	// Сравнение за постоянное время: обычное == утекает длину общего префикса.
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrPasswordMismatch
	}
	return nil
}

// ---------------------------------------------------------------------------
// Токены сессий и писем
// ---------------------------------------------------------------------------

// NewToken выдаёт случайный токен и его хеш.
//
// В базу кладётся ТОЛЬКО хеш: утечка таблицы сессий не даёт войти под чужой
// учёткой. Открытый токен существует ровно один раз — в ответе клиенту.
func NewToken() (raw string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("iam: не удалось получить случайные байты: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	return raw, HashToken(raw), nil
}

// HashToken считает хеш токена для поиска в базе.
func HashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// ---------------------------------------------------------------------------
// Коды: класс, персональный код ученика, служебный тег
// ---------------------------------------------------------------------------

// codeAlphabet — без визуально похожих символов: нет 0 и O, 1 и I и L.
// Ученик диктует код вслух и вводит его с телефона.
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// NewCode выдаёт код заданной длины из безопасного алфавита.
func NewCode(length int) (string, error) {
	if length < 4 {
		return "", fmt.Errorf("iam: длина кода должна быть не меньше 4, получено %d", length)
	}
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("iam: не удалось получить случайные байты: %w", err)
	}
	out := make([]byte, length)
	for i, b := range buf {
		// Смещение по модулю даёт незначительный перекос распределения;
		// для кода присоединения это несущественно, а энтропии хватает:
		// 31^6 ≈ 8.9e8 вариантов.
		out[i] = codeAlphabet[int(b)%len(codeAlphabet)]
	}
	return string(out), nil
}

// NewPublicTag выдаёт короткий тег вида #A7F2 для различения тёзок.
func NewPublicTag() (string, error) {
	c, err := NewCode(4)
	if err != nil {
		return "", err
	}
	return "#" + c, nil
}
