"""Private text-only SystemOne adapter for pinned Liquid decision weights."""
import os
import threading
from contextlib import asynccontextmanager

import torch
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from transformers import AutoModel

MODEL = os.environ["DECIDER_MODEL"]
DEVICE = os.environ["DECIDER_DEVICE"]
model = None
lock = threading.Lock()


@asynccontextmanager
async def lifespan(app):
    global model
    if DEVICE == "cuda" and not torch.cuda.is_available():
        raise RuntimeError("CUDA selected but unavailable to PyTorch")
    if DEVICE == "mps" and not torch.backends.mps.is_available():
        raise RuntimeError("Apple GPU selected but unavailable to PyTorch")
    dtype = torch.float32 if DEVICE == "cpu" else torch.bfloat16
    model = AutoModel.from_pretrained(
        MODEL, trust_remote_code=True, local_files_only=True, dtype=dtype
    ).to(DEVICE).eval()
    yield
    model = None


app = FastAPI(lifespan=lifespan)


class Request(BaseModel):
    state: str | dict | list
    questions: dict


@app.get("/health")
def health():
    return {"ok": model is not None, "model": MODEL, "device": DEVICE}


@app.post("/v1/systemone")
def evaluate(request: Request):
    if model is None:
        raise HTTPException(503, "Model is not ready")
    # One worker owns inference; canceled HTTP clients cannot create concurrent
    # GPU calls. No request can select a model, image URL, file, or executable.
    if not lock.acquire(blocking=False):
        raise HTTPException(503, "Inference is busy")
    try:
        with torch.inference_mode():
            result = model.system_one(request.state, request.questions)
    finally:
        lock.release()
    return {**result, "model": MODEL}
