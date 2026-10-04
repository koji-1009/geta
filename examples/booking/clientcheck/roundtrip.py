"""Round-trips the booking example through a client generated from its
OpenAPI document by openapi-python-client, an external generator.

The client raises on any status the document does not list, so a status on
the wire that the document omits fails this script. Tokens come from the
demo issuer's token endpoint, as any OAuth client gets them.

    uvx openapi-python-client generate --path openapi.json --output-path $OUT --overwrite
    go run . -addr 127.0.0.1:18180 -issuer-addr 127.0.0.1:18181 &
    uv run --with $OUT --with httpx python clientcheck/roundtrip.py http://127.0.0.1:18180 http://127.0.0.1:18181
"""

import datetime
import sys

import httpx
from geta_booking_example_client import AuthenticatedClient, Client
from geta_booking_example_client.api.default import (
    get_bookings_booking,
    get_bookings_booking_history,
    get_health,
    get_me,
    get_rooms,
    get_rooms_room,
    get_rooms_room_bookings,
    post_bookings_booking_changes,
    post_rooms,
    post_rooms_room_bookings,
)
from geta_booking_example_client.models import (
    Booking,
    BookingStatus,
    Cancel,
    Entry,
    GetRoomsFloor,
    History,
    NewBooking,
    NewRoom,
    NewRoomFloor,
    Problem,
    Rename,
    Reschedule,
    Room,
)


def expect(cond, what):
    if not cond:
        print("FAIL:", what)
        sys.exit(1)
    print("ok:", what)


def token(issuer, client_id):
    r = httpx.post(issuer + "/token", data={"grant_type": "client_credentials", "client_id": client_id,
                                           "client_secret": client_id + "-secret"})
    r.raise_for_status()
    return r.json()["access_token"]


def main():
    base, issuer = sys.argv[1], sys.argv[2]
    public = Client(base_url=base, raise_on_unexpected_status=True)
    desk = AuthenticatedClient(base_url=base, token=token(issuer, "frontdesk"), raise_on_unexpected_status=True)
    viewer = AuthenticatedClient(base_url=base, token=token(issuer, "viewer"), raise_on_unexpected_status=True)

    h = get_health.sync(client=public)
    expect(h is not None and h.status == "ok", "health is public")
    r = get_rooms.sync_detailed(client=public)
    expect(r.status_code == 401 and isinstance(r.parsed, Problem) and r.headers["www-authenticate"] == "Bearer",
           "no token is a documented 401 with a bare challenge")

    me = get_me.sync(client=desk)
    expect(me.subject == "frontdesk" and "rooms:write" in me.scopes, "the token's subject and scopes")

    nr = NewRoom(name="Hinoki", capacity=6, opens="08:00:00Z", closes="18:00:00Z", hourly_rate="2400.00", panel="192.0.2.20")
    r = post_rooms.sync_detailed(client=viewer, body=nr)
    expect(r.status_code == 403 and "insufficient_scope" in r.headers["www-authenticate"],
           "a token without rooms:write is a documented 403")
    r = post_rooms.sync_detailed(client=desk, body=nr)
    expect(r.status_code == 201 and isinstance(r.parsed, Room) and r.headers["location"] == "/rooms/" + str(r.parsed.id),
           "create a room: 201, Location, uuid id")
    room = r.parsed
    expect(room.hourly_rate == "2400.00" and room.panel == "192.0.2.20", "Money and ipv4 round-trip as strings")
    expect(isinstance(get_rooms_room.sync(room.id, client=viewer), Room), "fetch the room by its uuid")
    r = post_rooms.sync_detailed(client=desk, body=NewRoom(name="X", capacity=1, opens="08:00:00Z", closes="09:00:00Z", hourly_rate="12.5"))
    expect(r.status_code == 400 and isinstance(r.parsed, Problem), "Money's declared pattern is a documented 400")
    expect(room.floor == 1 and room.features == [], "a floor left out is its default, features none")

    # A repeated parameter, an integer enum, and a parameter with a default.
    # (The generator sends a deepObject flattened, min=4 for capacity[min],
    # so capacity is left to the Go tests.)
    r = post_rooms.sync_detailed(client=desk, body=NewRoom(name="Kaede", capacity=12, opens="08:00:00Z", closes="18:00:00Z",
                                                           hourly_rate="3000.00", floor=NewRoomFloor.VALUE_2, features=["projector", "video"]))
    expect(r.status_code == 201 and r.parsed.floor == 2, "create a room on floor 2 with features")
    lst = get_rooms.sync(client=viewer, feature=["projector", "video"], floor=GetRoomsFloor.VALUE_2, limit=5)
    expect([x.name for x in lst.items] == ["Kaede"], "list rooms by repeated feature and floor")
    r = get_rooms.sync_detailed(client=viewer, limit=500)
    expect(r.status_code == 400 and isinstance(r.parsed, Problem), "a limit past its maximum is a documented 400")

    day = datetime.date(2026, 10, 7)
    nb = NewBooking(title="Review", date=day, start="10:00:00Z", length="PT1H", organizer="ada@example.com", attendees=["bo@example.com"])
    r = post_rooms_room_bookings.sync_detailed(room.id, client=desk, body=nb)
    expect(r.status_code == 201 and isinstance(r.parsed, Booking) and r.parsed.price == "2400.00" and r.parsed.status == BookingStatus.CONFIRMED,
           "book: date, time, duration, email, priced by the room")
    b, tag = r.parsed, r.headers["etag"]
    r = post_rooms_room_bookings.sync_detailed(room.id, client=desk, body=nb)
    expect(r.status_code == 409 and r.parsed.type_ == "/problems/overlap", "an overlap is a documented 409 with its problem type")
    late = NewBooking(title="Late", date=day, start="17:30:00Z", length="PT1H", organizer="ada@example.com")
    r = post_rooms_room_bookings.sync_detailed(room.id, client=desk, body=late)
    expect(r.status_code == 422 and r.parsed.type_ == "/problems/outside-hours", "outside the hours is a documented 422 with its type")
    bad = NewBooking(title="T", date=day, start="12:00:00Z", length="PT1H", organizer="not-an-address")
    r = post_rooms_room_bookings.sync_detailed(room.id, client=desk, body=bad)
    expect(r.status_code == 400, "the application's email format is a documented 400")

    lst = get_rooms_room_bookings.sync(room.id, client=viewer, date=day)
    expect(len(lst.items) == 1 and lst.items[0].id == b.id, "list a day's bookings by a date query")

    r = get_bookings_booking.sync_detailed(b.id, client=viewer, if_none_match=tag)
    expect(r.status_code == 304, "a fetch holding the current tag is a documented 304")

    r = post_bookings_booking_changes.sync_detailed(b.id, client=desk, body=Rename(kind="rename", title="Design review"))
    expect(r.status_code == 428, "a change without If-Match is a documented 428")
    r = post_bookings_booking_changes.sync_detailed(b.id, client=desk, body=Rename(kind="rename", title="Design review"), if_match=tag)
    expect(r.status_code == 200 and r.parsed.title == "Design review", "rename with the tag held")
    stale, tag = tag, r.headers["etag"]
    r = post_bookings_booking_changes.sync_detailed(b.id, client=desk, body=Cancel(kind="cancel", reason="x"), if_match=stale)
    expect(r.status_code == 412, "a change against a stale tag is a documented 412")
    r = post_bookings_booking_changes.sync_detailed(b.id, client=desk, if_match=tag,
                                                    body=Reschedule(kind="reschedule", date=day, start="11:00:00Z", length="PT30M"))
    expect(r.status_code == 200 and r.parsed.start == "11:00:00Z" and r.parsed.price == "1200.00", "reschedule and reprice")
    tag = r.headers["etag"]
    r = post_bookings_booking_changes.sync_detailed(b.id, client=desk, body=Cancel(kind="cancel", reason="moved online"), if_match=tag)
    expect(r.status_code == 200 and r.parsed.status == BookingStatus.CANCELLED and r.parsed.cancel_reason == "moved online", "cancel")
    tag = r.headers["etag"]
    r = post_bookings_booking_changes.sync_detailed(b.id, client=desk, body=Rename(kind="rename", title="x"), if_match=tag)
    expect(r.status_code == 409 and r.parsed.type_ == "/problems/cancelled", "a cancelled booking takes no change: a documented 409")

    hist = get_bookings_booking_history.sync(b.id, client=viewer)
    expect(isinstance(hist, History) and [type(e.change).__name__ for e in hist.items] == ["Rename", "Reschedule", "Cancel"],
           "the history decodes each change into its variant")
    expect(all(isinstance(e, Entry) and e.by == "frontdesk" and e.at.tzinfo is not None for e in hist.items), "date-time and subject per entry")
    expect(hist.items[1].touched == ["/date", "/start", "/length", "/price"], "the members a change wrote, as JSON Pointers")

    r = desk.get_httpx_client().post("/rooms", content=b"name=x", headers={"Content-Type": "application/x-www-form-urlencoded"})
    expect(r.status_code == 415 and r.headers["accept"] == "application/json", "another media type is a 415 naming the one taken")
    r = desk.get_httpx_client().request("OPTIONS", "/rooms/" + str(room.id) + "/bookings")
    expect(r.status_code == 204 and r.headers["allow"] == "GET, HEAD, OPTIONS, POST", "OPTIONS lists what the path serves")
    print("round trip: all checks passed")


if __name__ == "__main__":
    main()
