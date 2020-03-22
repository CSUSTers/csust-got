#!/usr/bin/env bash

git pull
docker-compose down
docker-compose build
docker-compose up -d