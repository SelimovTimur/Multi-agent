import os
import unittest
from unittest.mock import patch

from app.config import Settings


class ConfigTests(unittest.TestCase):
    def test_direct_openrouter_defaults(self):
        with patch.dict(os.environ, {'OPENROUTER_API_KEY': 'test-key'}, clear=True):
            config = Settings.from_env()
        self.assertEqual(config.provider, 'openrouter')
        self.assertEqual(config.openrouter_base_url, 'https://openrouter.ai/api/v1')
        self.assertEqual(config.model, 'qwen/qwen3.7-plus')
        self.assertFalse(config.openrouter_reasoning_enabled)
        self.assertEqual(config.http_proxy, '')

    def test_explicit_transport_proxy(self):
        with patch.dict(os.environ, {'OPENROUTER_API_KEY': 'test-key',
                                     'LLM_HTTP_PROXY': 'http://host.docker.internal:10819'}, clear=True):
            self.assertEqual(Settings.from_env().http_proxy, 'http://host.docker.internal:10819')

    def test_old_proxy_key_is_not_sent_to_openrouter(self):
        with patch.dict(os.environ, {'PROXYAPI_API_KEY': 'old-key'}, clear=True):
            with self.assertRaisesRegex(ValueError, 'OPENROUTER_API_KEY'):
                Settings.from_env()
