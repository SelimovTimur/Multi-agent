package service

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"time"
)

// AudioService — конвертация аудио/видео в WAV 16kHz через ffmpeg.
//
// В Python это была функция convert_to_wav_16k(input_path, output_path),
// которая вызывала subprocess.run(["ffmpeg", ...]).
// Здесь то же самое — запускаем ffmpeg как внешний процесс.
type AudioService struct {
	maxDurationSec int // максимальная длительность (180 секунд = 3 минуты)
}

// Плейлисты и составные файлы могут читать адреса и чужие файлы сервера.
// Здесь только контейнеры записей, которые присылает браузер.
const localAudioFormats = "wav,mp3,mov,matroska,webm,ogg,flac,aac,amr,avi,mpeg,mpegts"

func NewAudioService(maxDurationSec int) *AudioService {
	return &AudioService{maxDurationSec: maxDurationSec}
}

// ConvertToWav16k конвертирует любой аудио/видеофайл в формат,
// который понимает ASR-модель: WAV, 16000 Hz, моно, 16-bit.
//
// Параметры ffmpeg:
//
//	-y              — перезаписать файл если существует
//	-i inputPath    — входной файл (mp3, wav, mp4, webm — что угодно)
//	-t 180          — обрезать до 3 минут
//	-ar 16000       — частота дискретизации 16 кГц (требование wav2vec2)
//	-ac 1           — моно (один канал)
//	-sample_fmt s16 — 16-битные семплы
//
// ctx позволяет прервать конвертацию если клиент отключился.
func (s *AudioService) ConvertToWav16k(ctx context.Context, inputPath, outputPath string) error {
	args := []string{
		"-nostdin",
		"-y",
		// Загруженный файл может оказаться плейлистом с адресами внутри.
		// Конвертация локальной записи не должна обращаться к сети сервера.
		"-protocol_whitelist", "file,pipe,crypto,data",
		"-format_whitelist", localAudioFormats,
		"-i", inputPath,
		"-t", strconv.Itoa(s.maxDurationSec),
		"-ar", "16000",
		"-ac", "1",
		"-sample_fmt", "s16",
		outputPath,
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.WaitDelay = 3 * time.Second
	// Диагностика ffmpeg может содержать адреса из недоверенного файла.
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("ошибка ffmpeg: %w", err)
	}
	return nil
}
