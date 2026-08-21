"""Entry point for a worker-only container: `python -m app.run_worker`."""

import logging
import signal
import threading

from app import engines, worker
from app.config import settings
from app.models import init_db

if __name__ == "__main__":
    logging.basicConfig(level=logging.INFO)
    init_db()
    if settings.preload_models:
        engines.warm_up()
    worker.start()

    finished = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: finished.set())
    signal.signal(signal.SIGINT, lambda *_: finished.set())
    finished.wait()
    worker.stop()
