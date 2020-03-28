# csust-got

[![Go Report](https://goreportcard.com/badge/github.com/csusters/csust-got)](https://goreportcard.com/report/github.com/csusters/csust-got)
[![codebeat badge](https://codebeat.co/badges/4d134b7f-e345-4378-b00d-7ab2177b94bc)](https://codebeat.co/projects/github-com-csusters-csust-got-master)

![GitHub language count](https://img.shields.io/github/languages/count/csusters/csust-got)
![GitHub](https://img.shields.io/github/license/csusters/csust-got)
![GitHub code size](https://img.shields.io/github/languages/code-size/csusters/csust-got)
![GitHub repo size](https://img.shields.io/github/repo-size/csusters/csust-got)
![GitHub issues](https://img.shields.io/github/issues/csusters/csust-got)
![GitHub closed issues](https://img.shields.io/github/issues-closed/csusters/csust-got)

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

Of course, you can also run script `docker-run.sh` to complete the upgrade.

```bash
./docker-run.sh
```

## Configuration

Please change configuration in `docker-compose.yml`.

Modify the `TOKEN` to your bot's token, or just set environment variable `CSUST_BOT_TOKEN`.

Please modify `REDIS_PASSWORD` in `docker-compose.yml`,and also please modify `requirepass` in `config/redis.conf`.
