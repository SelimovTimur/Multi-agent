"""HTTP-клиент нативного OpenAI Audio API для сервиса и бенчмарка."""

from __future__ import annotations

import json
import logging
import uuid
from typing import TYPE_CHECKING, Any
from urllib import error, request

from asr_review import (
    Recognition,
    build_recognition,
    confidence_from_logprobs,
    foreign_letters,
    is_tatar,
    replace_foreign_letters,
    tatar_script,
)

if TYPE_CHECKING:
    from asr_runtime import ASRConfig

logger = logging.getLogger(__name__)

# Подсказка для татарского. Параметр language=tt gpt-transcribe отвергает
# (400 «Language code 'tt' is not recognized»), язык задаётся только здесь.
# Орфография названа буквами, а образец написан татарской кириллицей: на
# коротких репликах одна английская подсказка не удерживала модель, и ответ
# ученика приходил турецким «Bilmeyim» или с казахскими і и ғ (05.10.2026).
# Слов урока и типовых ответов ученика («белмим», «аңламадым») в подсказке
# нет намеренно: модель склонна «слышать» слова из подсказки, они
# подтягивали бы распознанное к правильному ответу и прятали ошибки ученика.
_TATAR_PROMPT = (
    "The speech is in the Tatar language (tatar, татар теле). Write the "
    "transcript in Tatar Cyrillic only, with the letters ә, ө, ү, җ, ң, һ. "
    "It is not Kazakh, not Bashkir and not Turkish: never use Latin script. "
    "Example of Tatar text: Без татарча сөйләшәбез. Мин мәктәптә укыйм, "
    "ә син кайда яшисең? Җәй көне һава җылы һәм күңелле."
)
# Ученик ошибается, и разбору нужны именно его ошибки: модель не должна
# «улучшать» сказанное. Акцент при этом не ошибка — слово пишется по
# нормальной орфографии.
_TATAR_VERBATIM = (
    "Write down exactly what the speaker says, word for word. Keep the "
    "speaker's grammar and word choice mistakes: do not correct word forms, "
    "do not translate, do not add words. Spell each spoken word in standard "
    "Tatar orthography."
)
# Усиленная подсказка для единственного повтора, когда в тексте нашлись
# буквы чужого алфавита: весь татарский алфавит и явный запрет.
_TATAR_STRICT = (
    "Use only the Tatar Cyrillic alphabet: а ә б в г д е ё ж җ з и й к л м н ң "
    "о ө п р с т у ү ф х һ ц ч ш щ ъ ы ь э ю я. Never use the Kazakh letters "
    "қ ғ ұ і, the Bashkir letters ҡ ҙ ҫ, the Turkish letters ş ç ğ ı or any "
    "Latin letters."
)
_VERBATIM = (
    "Write down exactly what the speaker says, word for word, in the original "
    "language. Keep the speaker's mistakes: do not correct, do not translate, "
    "do not add words."
)


def build_prompt(language: str, *, strict: bool = False) -> str:
    """Подсказка распознаванию ответа ученика на языке урока."""
    if is_tatar(language):
        prompt = f"{_TATAR_PROMPT} {_TATAR_VERBATIM}"
        return f"{prompt} {_TATAR_STRICT}" if strict else prompt
    if language:
        return f"The audio is in {language}. {_VERBATIM}"
    return _VERBATIM


def _build_multipart_form_data(fields: dict[str, str], file_data: bytes) -> tuple[bytes, str]:
    boundary = uuid.uuid4().hex
    chunks: list[bytes] = []
    separator = f"--{boundary}".encode()
    field_names = list(fields.keys())
    for name in field_names:
        chunks.append(separator)
        chunks.append(f'Content-Disposition: form-data; name="{name}"'.encode())
        chunks.append(b"")
        chunks.append(str(fields[name]).encode())
    chunks.append(separator)
    chunks.append(b'Content-Disposition: form-data; name="file"; filename="audio.wav"')
    chunks.append(b"Content-Type: audio/wav")
    chunks.append(b"")
    body = b"\r\n".join(chunks) + b"\r\n" + file_data + b"\r\n"
    body += f"--{boundary}--\r\n".encode()
    content_type = f"multipart/form-data; boundary={boundary}"
    return body, content_type


def _extract_error_message(exc: Exception) -> str:
    if isinstance(exc, error.HTTPError):
        try:
            return exc.read().decode("utf-8")
        except Exception:
            return str(exc)
    return str(exc)


def _check_config(config: ASRConfig) -> None:
    if not config.api_key:
        raise RuntimeError("Ключ ASR API не найден. Укажите ASR_API_KEY или OPENAI_API_KEY")
    if not config.api_model:
        raise RuntimeError("ASR_API_MODEL не должен быть пустым")
    if config.api_timeout_seconds <= 0:
        raise RuntimeError("ASR_API_TIMEOUT_SECONDS должен быть положительным")


def _call_api(audio_bytes: bytes, config: ASRConfig, form_fields: dict[str, str]) -> dict[str, Any]:
    payload, content_type = _build_multipart_form_data(form_fields, audio_bytes)
    url = f"{config.api_base_url.rstrip('/')}/audio/transcriptions"
    req = request.Request(url=url, data=payload, method="POST")
    req.add_header("Authorization", f"Bearer {config.api_key}")
    req.add_header("Content-Type", content_type)
    req.add_header("Accept", "application/json")

    try:
        with request.urlopen(req, timeout=config.api_timeout_seconds) as resp:
            status = getattr(resp, "status", 200)
            if status < 200 or status >= 300:
                details = _extract_error_message(resp)
                raise RuntimeError(f"ASR API вернул статус {status}: {details}")
            content = resp.read()
    except Exception as exc:
        raise RuntimeError(f"Ошибка вызова ASR API: {_extract_error_message(exc)}") from exc

    try:
        response_payload = json.loads(content.decode("utf-8"))
    except Exception as exc:
        raise RuntimeError(
            f"ASR API вернул невалидный JSON: {content[:120]!r}"
        ) from exc
    if not isinstance(response_payload, dict):
        raise RuntimeError("ASR API вернул неверный формат ответа")
    return response_payload


def _payload_text(response_payload: dict[str, Any], *, allow_empty: bool) -> str:
    text = ""
    text_value = response_payload.get("text")
    if isinstance(text_value, str):
        text = text_value.strip()
    if not text:
        segments = response_payload.get("segments")
        if isinstance(segments, list):
            parts = []
            for segment in segments:
                if isinstance(segment, dict):
                    segment_text = segment.get("text")
                    if isinstance(segment_text, str):
                        parts.append(segment_text.strip())
            text = " ".join(part for part in parts if part).strip()
    if not text and not (allow_empty and isinstance(text_value, str)):
        raise RuntimeError("ASR API вернул пустой ответ")
    return text


def _payload_language(response_payload: dict[str, Any], configured: str) -> str:
    language = response_payload.get("language", configured)
    detected = response_payload.get("languages")
    if isinstance(detected, list) and detected and isinstance(detected[0], dict):
        language = detected[0].get("code", language)
    if not isinstance(language, str):
        language = configured or "tatar"
    return language


def transcribe(
    audio_bytes: bytes, config: ASRConfig, *, allow_empty: bool = False
) -> tuple[str, str]:
    """Только текст и язык, без вероятностей и повторов.

    Татарская речь получает ту же подсказку орфографии (без просьбы
    сохранить ошибки) и пишется только татарской кириллицей.
    """
    _check_config(config)
    form_fields: dict[str, str] = {
        "model": config.api_model,
        # Сервису нужен только текст; язык берётся из настройки, если API его
        # не возвращает отдельным полем.
        "response_format": "json",
    }
    tatar = is_tatar(config.api_language)
    if tatar:
        form_fields["prompt"] = _TATAR_PROMPT
    elif config.api_language:
        # Приложение хранит полные названия языков. gpt-transcribe принимает
        # language hints в отдельном формате, поэтому передаём мягкую подсказку.
        form_fields["prompt"] = (
            f"The audio is in {config.api_language}. "
            "Transcribe it in the original language."
        )
    response_payload = _call_api(audio_bytes, config, form_fields)
    text = _payload_text(response_payload, allow_empty=allow_empty)
    if tatar:
        text = tatar_script(text)
    return text, _payload_language(response_payload, config.api_language)


def _recognize_once(
    audio_bytes: bytes, config: ASRConfig, language: str, *, strict: bool, logprobs: bool
) -> dict[str, Any]:
    form_fields = {
        "model": config.api_model,
        "response_format": "json",
        "prompt": build_prompt(language, strict=strict),
    }
    if logprobs:
        # Вероятности токенов — единственный сигнал «модель не уверена»,
        # который даёт API. Работает вместе с response_format=json.
        form_fields["include[]"] = "logprobs"
    return _call_api(audio_bytes, config, form_fields)


def transcribe_detailed(
    audio_bytes: bytes,
    config: ASRConfig,
    *,
    language: str | None = None,
    allow_empty: bool = False,
) -> Recognition:
    """Распознавание ответа с уверенностью и решением о проверке текста.

    language — язык урока («tatar», «english», …); None — ASR_API_LANGUAGE.
    Для татарского буквы чужого алфавита дают один повтор с усиленной
    подсказкой, а если не помог — однозначную замену и проверку учеником.
    """
    _check_config(config)
    lang = (config.api_language if language is None else language).strip().lower()

    logprobs = True
    try:
        response_payload = _recognize_once(audio_bytes, config, lang, strict=False, logprobs=True)
    except RuntimeError as exc:
        # Модель без вероятностей токенов (например, whisper-1) отвечает 400
        # на include[]=logprobs, а распознавание с вероятностями заметно
        # дольше, и на долгой записи провайдер отвечал пустым 500. В обоих
        # случаях распознаём ещё раз без них: уверенности не будет, и текст
        # уйдёт ученику на проверку, но голосовой ответ не сломан. Прочие
        # отказы (предел частоты, неверный запрос) повтор не исправит.
        status = getattr(exc.__cause__, "code", None)
        unsupported = "logprob" in str(exc).lower()
        if not unsupported and not (isinstance(status, int) and status >= 500):
            raise
        logger.warning("Распознавание с вероятностями токенов не удалось (%s), повторяю без них",
                       "модель их не отдаёт" if unsupported else f"провайдер ответил {status}")
        logprobs = False
        response_payload = _recognize_once(audio_bytes, config, lang, strict=False, logprobs=False)
    text = _payload_text(response_payload, allow_empty=allow_empty)
    confidence, min_prob = confidence_from_logprobs(response_payload.get("logprobs"))
    attempts = 1

    foreign = is_tatar(lang) and bool(foreign_letters(text))
    if foreign:
        # Обычно модель на коротком ответе просто спутала близкий тюркский
        # язык, и с усиленной подсказкой второй раз пишет по-татарски. Больше
        # одного повтора — двойная цена без пользы. Сбой повтора не роняет
        # ответ: первый текст у нас уже есть.
        attempts = 2
        try:
            retry_payload = _recognize_once(
                audio_bytes, config, lang, strict=True, logprobs=logprobs
            )
            retry_text = _payload_text(retry_payload, allow_empty=True)
        except RuntimeError as exc:
            logger.warning("Повтор распознавания не удался, остаётся первый ответ: %s", exc)
        else:
            if retry_text:
                text = retry_text
                confidence, min_prob = confidence_from_logprobs(retry_payload.get("logprobs"))
                foreign = bool(foreign_letters(text))
        if foreign:
            text = replace_foreign_letters(text)

    return build_recognition(
        text=text,
        # Платформа хранит полные названия языков, поэтому запрошенный язык
        # важнее кода, угаданного API.
        language=lang or _payload_language(response_payload, ""),
        confidence=confidence,
        min_token_prob=min_prob,
        foreign_letters_left=foreign,
        audio_bytes=audio_bytes,
        config=config,
        attempts=attempts,
    )


def transcribe_material(
    audio_bytes: bytes,
    config: ASRConfig,
    *,
    language: str | None = None,
    allow_empty: bool = False,
) -> Recognition:
    """Распознавание материала учителя: запись урока, ролик по ссылке.

    Проверка текста здесь не нужна — его правит учитель на следующем шаге, —
    поэтому без вероятностей токенов и без повторов: с ними распознавание
    вдвое дольше, и на записи в несколько минут провайдер отвечал пустым 500
    («Не удалось распознать речь» на ролике по ссылке). Подсказка — та же
    татарская орфография, что и у ответа ученика, а буквы чужого алфавита
    заменяются: казахские буквы в материале урока так же неуместны.
    """
    _check_config(config)
    lang = (config.api_language if language is None else language).strip().lower()
    response_payload = _recognize_once(audio_bytes, config, lang, strict=False, logprobs=False)
    text = _payload_text(response_payload, allow_empty=allow_empty)
    if is_tatar(lang) and foreign_letters(text):
        text = replace_foreign_letters(text)
    return Recognition(text=text, language=lang or _payload_language(response_payload, ""))
