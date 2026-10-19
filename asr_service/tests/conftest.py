import pytest

import asr_api
from asr_fakes import FakeAPI
from asr_runtime import ASRConfig


@pytest.fixture(autouse=True)
def _cpu_device(monkeypatch):
    # Без ASR_DEVICE ASRConfig.from_env спрашивает torch про видеокарту, а
    # импорт torch — десятки секунд. Устройство тестам не важно.
    monkeypatch.setenv("ASR_DEVICE", "cpu")


@pytest.fixture
def fake_api(monkeypatch):
    api = FakeAPI()
    monkeypatch.setattr(asr_api.request, "urlopen", api.urlopen)
    return api


@pytest.fixture
def config():
    # Пороги по умолчанию — те, что уходят в прод без настройки.
    return ASRConfig(
        use_api=True,
        api_key="test-key",
        api_base_url="http://asr.test/v1/",
        api_language="tatar",
    )
