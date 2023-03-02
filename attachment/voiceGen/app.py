from flask import Flask, request, jsonify, render_template, url_for, redirect
from flask_restful.reqparse import RequestParser


app = Flask(__name__)
app.config.from_object("config")
app.secret_key = 'd25ba7735e13a52601fd339fd28f9d869b5ba3759e9e464d3911224820f3a22a'


@app.route('/GenShin/GetVoice')
def giveBackURL():
    if (request.method == "GET"):
        # 从./db/character.db中随机取出一条数据，以json返回
        import sqlite3
        conn = sqlite3.connect('/var/www/character.db')
        c = conn.cursor()
        c.execute(
            "SELECT * FROM character WHERE id >= (ABS(RANDOM()) % (SELECT MAX(id) FROM character)) LIMIT 1;")
        result = c.fetchone()
        conn.close()
        # 生成返回的json
        return jsonify({
            'character': result[1],
            'topic': result[2],
            'text': result[3],
            'audio': result[4],
        })


@app.route('/GenShin/GetVoice/v2')
def giveBackURLv2():
    if (request.method == "GET"):
        try:
            parser = RequestParser()
            parser.add_argument('character', location='args', required=False)
            parser.add_argument('topic', location='args', required=False)
            parser.add_argument('text', location='args', required=False)
            parser.add_argument('sex', location='args', required=False)
            parser.add_argument('type', location='args', required=False)
            args = parser.parse_args()
            print(args)
            # 替换掉空参数
            if args['character'] == None:
                args['character'] = ''
            if args['topic'] == None:
                args['topic'] = ''
            if args['text'] == None:
                args['text'] = ''
            if args['sex'] == None:
                args['sex'] = ''
            if args['type'] == None:
                args['type'] = ''

            import sqlite3
            conn = sqlite3.connect('/var/www/genshinVoice.db')
            c = conn.cursor()

            # 如果没有参数，随机返回一条数据
            if args['character'] == None and args['topic'] == None and args['text'] == None:
                c.execute(
                    "SELECT * FROM character WHERE id >= (ABS(RANDOM()) % (SELECT MAX(id) FROM character)) LIMIT 1;")
                result = c.fetchone()
                conn.close()
                return jsonify(result)
            # 如果有参数，根据参数返回数据
            else:
                sql = "SELECT * FROM character WHERE npcNameLocal like ? AND topic like ? AND text like ? AND sex like ? AND type like ?"

                argsList = (args['character'], args['topic'],
                            args['text'], args['sex'], args['type'])

                c.execute(sql, argsList)

                # 从result中随机取出一条数据
                import random
                result = c.fetchall()
                print(len(result))
                # 如果没有数据，返回404
                if len(result) == 0:
                    conn.close()
                    return jsonify({'text': '进不去……'}), 404

                result = random.choice(result)
                conn.close()
                return jsonify({
                    'npcNameLocal': result[1],
                    'sex': result[2],
                    'type': result[3],
                    'topic': result[4],
                    'text': result[5],
                    'npcNameCode': result[6],
                    'language': result[7],
                    'fileName': result[8],
                    'audioURL': result[9],
                })
        except Exception as e:
            print(e)
            return jsonify({'text': '进不去……', 'err': str(e)}), 404


def run():
    app.run(host="*0.0.0.0", port=8000)


if __name__ == '__main__':

    app.run(host="127.0.0.1", port=8000)
