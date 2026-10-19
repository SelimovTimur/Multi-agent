FROM python:3.10-slim

WORKDIR /app

ENV PYTHONUNBUFFERED=1 TTS_USE_API=true TTS_DEVICE=cpu

RUN apt-get update && apt-get install -y --no-install-recommends libsndfile1 \
    && rm -rf /var/lib/apt/lists/*

# Синтез идёт через API: локальной модели Silero и torch в образе нет.
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY server.py tts_settings.py ./

EXPOSE 8000
CMD ["python", "server.py"]
