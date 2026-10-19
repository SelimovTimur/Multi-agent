FROM python:3.10-slim

WORKDIR /app

ENV PYTHONUNBUFFERED=1

RUN apt-get update && apt-get install -y --no-install-recommends libsndfile1 \
    && rm -rf /var/lib/apt/lists/*

# Распознавание идёт через API: локальной модели и torch в образе нет.
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY asr_settings.py server.py asr_runtime.py asr_api.py asr_review.py audio_quality.py ./

EXPOSE 8000
CMD ["python", "server.py"]
