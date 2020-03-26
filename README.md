# csust-got

[![Go Report](https://goreportcard.com/badge/github.com/csusters/csust-got)](https://goreportcard.com/report/github.com/csusters/csust-got)
[![codebeat badge](https://codebeat.co/badges/4d134b7f-e345-4378-b00d-7ab2177b94bc)](https://codebeat.co/projects/github-com-csusters-csust-got-master)
![GitHub language count](https://img.shields.io/github/languages/count/csusters/csust-got)

csust new telegram bot in go

## Deploy

You need to install Docker first.

Clone the project.

```bash
git pull git@github.com:CSUSTers/csust-got.git
```

Then run it with docker compose.

```bash
docker-compose up -d
``` 

## Upgrade from old version

Clone the newest version.

```bash
git pull
```

Rebuild.

```bash
docker-compose build
```

Then run it.

```bash
docker-compose up -d
``` 

Also, you may use `docker-run.sh` to upgrade.

```bash
./docker-run.sh
```

## Configuration

Please change configuration in `docker-compose.yml`.

Change `TOKEN` to your bot token, or just set environment `CSUST_BOT_TOKEN` to your bot token.

Please change `REDIS_PASSWORD` in `docker-compose.yml`,and you should also change `requirepass` in `config/redis.conf`.
