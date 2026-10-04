"""A door panel: holds a room's live feed open over WebSocket, with the
`websockets` library, and prints each booking change as it arrives.

    uv run --with websockets --with httpx python clientcheck/panel.py http://127.0.0.1:18180 http://127.0.0.1:18181 ROOM [COUNT]

It exits after COUNT messages (default 1), or after 30 seconds with status 1.
"""

import asyncio
import json
import sys

import httpx
import websockets


async def main():
    base, issuer, room = sys.argv[1], sys.argv[2], sys.argv[3]
    count = int(sys.argv[4]) if len(sys.argv) > 4 else 1
    r = httpx.post(issuer + "/token", data={"grant_type": "client_credentials", "client_id": "viewer", "client_secret": "viewer-secret"})
    r.raise_for_status()
    url = "ws" + base.removeprefix("http") + "/rooms/" + room + "/live"

    try:
        await websockets.connect(url)
    except websockets.InvalidStatus as e:
        print("without a token:", e.response.status_code)

    async with websockets.connect(url, additional_headers={"Authorization": "Bearer " + r.json()["access_token"]}) as ws:
        print("connected:", ws.response.status_code, ws.response.headers["Sec-WebSocket-Accept"])
        pong = await ws.ping()
        await pong
        print("ping answered")
        for _ in range(count):
            msg = json.loads(await asyncio.wait_for(ws.recv(), 30))
            print("update:", msg["kind"], msg["title"], msg["date"], msg["start"])


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except TimeoutError:
        print("no update within 30 seconds")
        sys.exit(1)
