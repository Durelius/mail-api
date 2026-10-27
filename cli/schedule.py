#!/usr/bin/env python3
"""Schedule a reminder mail through mail-api, asking for each value in turn.

mail-api only listens on the server itself, so the request goes over SSH.
Settings live in ~/.config/mail-api/config.json:

    {
      "host": "user@server",
      "ssh_key": "~/.ssh/id_ed25519",
      "default_to": "you@example.com",
      "from": "noreply@example.com",
      "from_name": "Reminder",
      "calendar": "Home"
    }

and the API key in ~/.config/mail-api/key. With "calendar" set, it also offers
to add the reminder to that macOS Calendar (via osascript; macOS asks once for
permission to control Calendar).
"""

import json
import re
import subprocess
import sys
from datetime import datetime, timedelta
from pathlib import Path
from typing import Optional

try:
    import readline  # noqa: F401  (arrow keys and history in input())
except ImportError:
    pass

CONFIG_DIR = Path.home() / ".config" / "mail-api"
WEEKDAYS = ["monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"]

TIME_HELP = """  Examples: 18:00 | tomorrow 09:00 | friday 14:30 | 2026-11-01 09:00 | in 30m | in 2h | in 3d"""


def die(msg: str) -> None:
    print(f"error: {msg}", file=sys.stderr)
    sys.exit(1)


def load_config() -> dict:
    path = CONFIG_DIR / "config.json"
    if not path.exists():
        die(f"missing {path}, see the top of {Path(__file__).name}")
    cfg = json.loads(path.read_text())
    if "host" not in cfg:
        die(f"{path} needs a host")
    key_path = CONFIG_DIR / "key"
    if not key_path.exists():
        die(f"missing API key in {key_path}")
    cfg["key"] = key_path.read_text().strip()
    return cfg


def parse_clock(text: str) -> Optional[tuple]:
    m = re.fullmatch(r"(\d{1,2})(?:[:.](\d{2}))?", text)
    if not m:
        return None
    hour, minute = int(m.group(1)), int(m.group(2) or 0)
    if hour > 23 or minute > 59:
        return None
    return hour, minute


def parse_when(text: str, now: datetime) -> Optional[datetime]:
    """Turn what the user typed into a local datetime, or None if unclear."""
    text = text.strip().lower()

    m = re.fullmatch(r"(?:in |\+)\s*(\d+)\s*(m|min|mins|minutes?|h|hours?|d|days?)", text)
    if m:
        n, unit = int(m.group(1)), m.group(2)[0]
        delta = {"m": timedelta(minutes=n), "h": timedelta(hours=n), "d": timedelta(days=n)}[unit]
        return now + delta

    m = re.fullmatch(r"(\d{4}-\d{2}-\d{2})[ t](\S+)", text)
    if m:
        clock = parse_clock(m.group(2))
        if not clock:
            return None
        try:
            day = datetime.strptime(m.group(1), "%Y-%m-%d")
        except ValueError:
            return None
        return day.replace(hour=clock[0], minute=clock[1])

    m = re.fullmatch(r"(today|tonight|tomorrow|" + "|".join(WEEKDAYS) + r")\s+(\S+)", text)
    if m:
        clock = parse_clock(m.group(2))
        if not clock:
            return None
        word = m.group(1)
        if word in ("today", "tonight"):
            days = 0
        elif word == "tomorrow":
            days = 1
        else:
            days = (WEEKDAYS.index(word) - now.weekday()) % 7 or 7
        day = now + timedelta(days=days)
        return day.replace(hour=clock[0], minute=clock[1], second=0, microsecond=0)

    clock = parse_clock(text)
    if clock:
        at = now.replace(hour=clock[0], minute=clock[1], second=0, microsecond=0)
        # A bare time that has already passed today means tomorrow.
        return at if at > now else at + timedelta(days=1)

    return None


def ask(prompt: str, default: Optional[str] = None, required: bool = True) -> str:
    suffix = f" [{default}]" if default else ""
    while True:
        answer = input(f"{prompt}{suffix}: ").strip()
        if answer:
            return answer
        if default is not None:
            return default
        if not required:
            return ""
        print("  This one is required.")


def ask_when() -> datetime:
    print(TIME_HELP)
    while True:
        now = datetime.now().astimezone()
        text = ask("When")
        at = parse_when(text, now)
        if at is None:
            print("  Couldn't read that time.")
            continue
        at = at.astimezone()
        if at <= now:
            print("  That time has already passed.")
            continue
        if yes(f"  -> {at:%A %Y-%m-%d %H:%M} ({at:%Z}), ok?", default=True):
            return at


def ask_message(default: str) -> str:
    print("Message (finish with an empty line, or press Enter right away to use the subject):")
    lines = []
    while True:
        line = input("  > " if not lines else "  . ")
        if not line:
            break
        lines.append(line)
    return "\n".join(lines) if lines else default


def yes(prompt: str, default: bool = False) -> bool:
    """Yes/No picker: arrow keys move, Enter picks, y/n pick directly."""
    if not sys.stdin.isatty():
        hint = "Y/n" if default else "y/N"
        answer = ask(f"{prompt} ({hint})", required=False).lower()
        return default if not answer else answer in ("y", "yes")

    import termios
    import tty

    choice = default
    fd = sys.stdin.fileno()

    def draw() -> None:
        on, off = "\x1b[7m", "\x1b[0m"  # reverse video marks the selection
        y = f"{on} Yes {off}" if choice else " Yes "
        n = f"{on} No {off}" if not choice else " No "
        sys.stdout.write(f"\r\x1b[2K{prompt}  {y}  {n}")
        sys.stdout.flush()

    old = termios.tcgetattr(fd)
    try:
        tty.setcbreak(fd)
        draw()
        while True:
            ch = sys.stdin.read(1)
            if ch == "\x1b":
                seq = sys.stdin.read(2)
                if seq in ("[C", "[D", "[A", "[B"):  # right, left, up, down
                    choice = not choice
                    draw()
            elif ch in ("\t", " "):
                choice = not choice
                draw()
            elif ch.lower() in ("y", "n"):
                choice = ch.lower() == "y"
                draw()
                break
            elif ch in ("\r", "\n"):
                break
            elif ch == "\x03":
                raise KeyboardInterrupt
            elif ch == "\x04":
                raise EOFError
    finally:
        termios.tcsetattr(fd, termios.TCSADRAIN, old)
        sys.stdout.write("\n")
    return choice


# Values arrive as arguments rather than being pasted into the script, so
# quotes in a subject can't break it. The date is built field by field because
# parsing a date string in AppleScript depends on the system locale.
CALENDAR_SCRIPT = """
on run argv
    set {calName, evTitle, evNotes, y, mo, d, h, mi, mins} to argv
    set startDate to current date
    set day of startDate to 1
    set year of startDate to (y as integer)
    set month of startDate to (mo as integer)
    set day of startDate to (d as integer)
    set time of startDate to ((h as integer) * 3600 + (mi as integer) * 60)
    set endDate to startDate + (mins as integer) * minutes
    tell application "Calendar"
        tell calendar calName
            set e to make new event at end with properties {summary:evTitle, start date:startDate, end date:endDate, description:evNotes}
            make new display alarm at end of display alarms of e with properties {trigger interval:0}
        end tell
    end tell
end run
"""


def add_to_calendar(calendar: str, title: str, notes: str, at: datetime, minutes: int = 15) -> Optional[str]:
    """Create an event with an alert at its start. Returns an error message, or None."""
    local = at.astimezone()
    args = [calendar, title, notes, str(local.year), str(local.month), str(local.day),
            str(local.hour), str(local.minute), str(minutes)]
    result = subprocess.run(["osascript", "-", *args], input=CALENDAR_SCRIPT, capture_output=True, text=True)
    if result.returncode != 0:
        return result.stderr.strip() or f"osascript exited with {result.returncode}"
    return None


def schedule(cfg: dict, payload: dict) -> dict:
    remote = (
        "read -r K; curl -sS -X POST http://127.0.0.1:5300/mail/schedule "
        '-H "X-Api-Key: $K" -H "Content-Type: application/json" --data-binary @-'
    )
    cmd = ["ssh", "-o", "BatchMode=yes", "-o", "LogLevel=ERROR"]
    if cfg.get("ssh_key"):
        cmd += ["-i", str(Path(cfg["ssh_key"]).expanduser())]
    cmd += [cfg["host"], remote]
    stdin = cfg["key"] + "\n" + json.dumps(payload)
    result = subprocess.run(cmd, input=stdin, capture_output=True, text=True)
    if result.returncode != 0:
        die(result.stderr.strip() or f"ssh exited with {result.returncode}")
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError:
        die(f"unexpected answer from mail-api: {result.stdout.strip()}")


def main() -> None:
    cfg = load_config()
    try:
        at = ask_when()
        to = ask("To", cfg.get("default_to"))
        subject = ask("Subject")
        text = ask_message(subject)
    except (KeyboardInterrupt, EOFError):
        print("\nCanceled, nothing scheduled.")
        sys.exit(130)

    print()
    print(f"  When:    {at:%A %Y-%m-%d %H:%M %Z}")
    print(f"  To:      {to}")
    print(f"  Subject: {subject}")
    print("  Message: " + text.replace("\n", "\n           "))
    try:
        if not yes("Schedule it?", default=True):
            print("Canceled, nothing scheduled.")
            return
    except (KeyboardInterrupt, EOFError):
        print("\nCanceled, nothing scheduled.")
        sys.exit(130)

    payload = {
        "to": [to],
        "subject": subject,
        "text": text + "\n",
        "sendAt": at.isoformat(timespec="seconds"),
    }
    if cfg.get("from"):
        payload["from"] = cfg["from"]
    if cfg.get("from_name"):
        payload["fromName"] = cfg["from_name"]

    resp = schedule(cfg, payload)
    if not resp.get("success"):
        die(resp.get("message", "mail-api refused it"))
    print(f"Scheduled #{resp['id']} for {at:%A %H:%M}. It goes out within 15 minutes of that time.")

    calendar = cfg.get("calendar")
    if not calendar:
        return
    try:
        add = yes(f"Add to Calendar ({calendar})?", default=True)
    except (KeyboardInterrupt, EOFError):
        print()
        return
    if add:
        notes = f"{text}\n\n(mail-api reminder #{resp['id']})"
        err = add_to_calendar(calendar, subject, notes, at)
        print(f"Couldn't add it to Calendar: {err}" if err else f"Added to Calendar ({calendar}) with an alert at {at:%H:%M}.")


if __name__ == "__main__":
    main()
