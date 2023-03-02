

class CharacterDTO:
    # 文件名
    fileName = ''
    # 不同语言的名字
    npcNameLocal = ''
    # 对话文本
    text = ''
    # 对话类型
    type = ''
    # 对话主题
    topic = ''
    # 代码中的名字
    npcNameCode = ''
    # 文本语言
    language = ''
    # url
    audioURL = ''
    # 性别
    sex = ''

    def printAll(self):
        print(self.fileName, self.npcNameLocal, self.text, self.type, self.topic, self.npcNameCode, self.language, self.audioURL, self.sex)


# 从result.py读取数据，并写入到./db/genshinVoice.db中
def writeData():
    import sqlite3
    conn = sqlite3.connect('./db/genshinVoice.db')
    c = conn.cursor()
    c.execute("DROP TABLE IF EXISTS character")
    c.execute("CREATE TABLE character (id INTEGER PRIMARY KEY AUTOINCREMENT, npcNameLocal TEXT, sex BOOL, type TEXT, topic TEXT, text TEXT, npcNameCode TEXT, language TEXT, fileName TEXT, audioURL TEXT)")
    
    import json
    with open('./db/result.json', 'r', encoding='utf-8') as f:
        data = json.load(f)
        for i in data:
            if data.get(i).get('language') == 'CHS':
                chadto = CharacterDTO()
                chadto.language = data.get(i).get('language')
                chadto.fileName = data.get(i).get('fileName')
                chadto.npcNameLocal = data.get(i).get('npcName')
                chadto.text = data.get(i).get('text')
                chadto.type = data.get(i).get('type')

                fileInfo = chadto.fileName.split('\\')
                # 拿到codename
                chadto.npcNameCode = fileInfo[-2].lower().replace('vo_', '')
                # 如果是futter对话，拿到topic
                if chadto.type == 'Fetter':
                    chadto.topic = '_'.join(fileInfo[-1].split('.')[0].split('_')[-2:])
                # 转换url
                chadto.audioURL = 'https://api.csu.st/file/' + '/'.join(chadto.fileName.split('\\')[1:]).replace('.wem', '.ogg')
                # 查表填入性别
                type_one_list = ['荧', '七七', '丽莎', '九条裟罗', '云堇', '优菈', '八重神子', '凝光', '刻晴', '北斗', '可莉', '埃洛伊', '安柏', '宵宫', '早柚', '烟绯', '珊瑚宫心海', '琴', '甘雨', '申鹤', '砂糖', '神里绫华', '罗莎莉亚', '胡桃', '芭芭拉', '莫娜', '菲谢尔', '诺艾尔', '辛焱', '迪奥娜', '雷电将军', '香菱', '瑶瑶', '珐露珊', '莱依拉', '纳西妲', '妮露', '坎蒂丝', '多莉', '柯莱', '夜兰', '久岐忍', '鹿野院平藏']
                type_two_list = ['空','五郎', '凯亚', '托马', '枫原万叶', '班尼特', '神里绫人', '荒泷一斗', '行秋', '达达利亚', '迪卢克', '重云', '钟离', '阿贝多', '雷泽', '魈', '艾尔海森', '流浪者', '赛诺', '温迪', '提纳里', '']

                if chadto.npcNameLocal in type_one_list:
                    chadto.sex = True
                if chadto.npcNameLocal in type_two_list:
                    chadto.sex = False

                # 将chadto写入到数据库中
                sql = "INSERT INTO character (npcNameLocal, sex, type, topic, text, npcNameCode, language, fileName, audioURL) VALUES ('{}', '{}', '{}', '{}','{}', '{}', '{}', '{}','{}')".format(
                    chadto.npcNameLocal, 
                    chadto.sex, 
                    chadto.type, 
                    chadto.topic, 
                    chadto.text, 
                    chadto.npcNameCode, 
                    chadto.language, 
                    chadto.fileName, 
                    chadto.audioURL
                )
                c.execute(sql)
                conn.commit()
    conn.close()

if __name__ == '__main__':
    writeData()