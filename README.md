# mail-api

Small Go service that sends mail for the sites on the Linode VPS. It hands
mail to the local Postfix on `127.0.0.1:25`, which trusts localhost and
DKIM-signs for `wilhelm.my`, so it needs no SMTP password.

It listens on `127.0.0.1:5300` only.

## Endpoints

### `POST /mail/send` (backends on the VPS)

Needs `X-Api-Key`. Sends to any address, from any address at
`ALLOWED_FROM_DOMAINS`.

```json
{
  "from": "admin@wilhelm.my",
  "fromName": "Helptype",
  "to": ["someone@example.com"],
  "replyTo": "optional@example.com",
  "subject": "Hello",
  "text": "plain body",
  "html": "<p>html body</p>"
}
```

`from` defaults to `DEFAULT_FROM`. Give `text`, `html` or both.

### `POST /mail/schedule` (delayed mail, backends on the VPS)

Same key and body as `/mail/send`, plus `sendAt` in RFC 3339, at most a year
ahead:

```json
{
  "to": ["someone@example.com"],
  "subject": "Reminder",
  "text": "Your booking is tomorrow.",
  "sendAt": "2026-11-01T09:00:00+01:00"
}
```

Answers `201` with `{"id": 12, ...}`. The mail is stored in SQLite
(`DB_PATH`) and sent by a check that runs at start-up and every
`CHECK_INTERVAL` (default `15m`), so it goes out up to that long after
`sendAt`. A send that fails is retried on the next check, up to 5 times. The
`X-Mail-Test` header is stored with the mail and applied when it is sent.

### `DELETE /mail/schedule/{id}`

Cancels a mail that hasn't been sent yet. Needs the key of the client that
scheduled it; answers `404` if there is no such pending mail.

### `POST /mail/contact/{site}` (public contact forms)

No key. The recipient comes from `sites.json`, never from the request, so the
endpoint can't be used to send mail to arbitrary people. Rate limited to 5
requests per 10 minutes per client IP. Apache proxies it, e.g. on
djcarldurelius.se:

```apache
ProxyPass        /mail/contact/ http://127.0.0.1:5300/mail/contact/
ProxyPassReverse /mail/contact/ http://127.0.0.1:5300/mail/contact/
```

```json
{ "name": "", "email": "guest@example.com", "phone": "070...", "message": "..." }
```

`email` or `phone` is required; `email` becomes the Reply-To.

### `GET /mail/health`

## Test mode

Add an `X-Mail-Test` header (any value) to any sending endpoint and the mail goes to
`TEST_RECIPIENT` (`test@wilhelm.my`) instead of the real recipients. The
subject becomes `[TEST to <real recipients>] ...` and the real recipients are
kept in an `X-Original-To` header.

## `schedule` CLI

`cli/schedule.py` schedules a reminder from your own machine. It asks for the
time (`18:00`, `tomorrow 09:00`, `friday 14:30`, `in 2h`, ...), recipient,
subject and message one at a time, sends them to `POST /mail/schedule` over
SSH, and can also add the reminder to macOS Calendar with an alert. Standard
library only.

```bash
ln -s "$PWD/cli/schedule.py" ~/.local/bin/schedule
```

It reads `~/.config/mail-api/config.json` (host, SSH key, defaults and
calendar; see the top of the script) and the API key from
`~/.config/mail-api/key`.

## Config

Environment, see `deploy/mail-api.env.example`. Contact sites live in the file
named by `SITES_FILE`, see `deploy/sites.example.json`. On the VPS both are in
`/etc/mail-api/`.

## Deploy

```bash
HOST=user@server ./deploy.sh
```

`SSH_KEY` defaults to `~/.ssh/id_ed25519_nopass`.

Runs the tests, builds a linux/amd64 binary, installs it to
`/usr/local/bin/mail-api` with `deploy/mail-api.service`, and restarts it.
Config in `/etc/mail-api/` is not touched.

First-time setup on the VPS (already done):

```bash
sudo useradd --system --no-create-home --shell /sbin/nologin mailapi
sudo mkdir /etc/mail-api   # then add mail-api.env and sites.json
sudo chown -R root:mailapi /etc/mail-api && sudo chmod 750 /etc/mail-api && sudo chmod 640 /etc/mail-api/*
sudo systemctl enable mail-api
```

`test@wilhelm.my` is an alias in `/etc/aliases`.
