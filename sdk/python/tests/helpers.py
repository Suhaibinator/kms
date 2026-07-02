from __future__ import annotations

import time
from typing import Callable


def wait_until(pred: Callable[[], bool], timeout: float = 5.0, interval: float = 0.02) -> bool:
    """Poll pred until true or timeout. Returns the final truth value."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if pred():
            return True
        time.sleep(interval)
    return pred()
