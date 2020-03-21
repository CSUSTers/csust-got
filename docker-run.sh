git pull
docker build -t csust-bot .
docker container stop bot
docker container rm bot
docker container run --name=bot -d csust-bot