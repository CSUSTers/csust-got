import pprint
from typing import Self

import httpx


class TrieNode:
    leaf: bool
    children: dict[str, Self]

    def __init__(self):
        self.leaf = False
        self.children = {}

    def insert(self, s: str):
        if not s:
            self.leaf = True
            return

        ch = s[0]
        if ch in self.children:
            self.children[ch].insert(s[1:])
        else:
            node = TrieNode()
            node.insert(s[1:])
            self.children[ch] = node

    def to_regex(self, greedy=True) -> str:
        xs = self._to_regex(greedy)
        match len(xs):
            case 0:
                return ""
            case 1:
                return xs[0]
            case _:
                return '|'.join(xs)

    def _to_regex(self, greedy=True) -> list[str]:
        if not self.children:
            return []
        else:
            suffix = '?' if greedy else '??'
            ret = []
            items = [item for item in self.children.items()]
            for ch, node in items:
                xs = node._to_regex(greedy)
                match len(xs):
                    case 0:
                        follow = ""
                    case 1:
                        follow = xs[0]
                    case _:
                        follow = '(?:' + '|'.join(xs) + ')'
                ret.append(ch+follow)
            if self.leaf and ret:
                if len(ret) == 1 and len(ret[0]) == 1:
                    return [ret[0]+suffix]
                return [f'(?:{"|".join(ret)}){suffix}']
            return ret


    def dict(self):
        return {
            'leaf': self.leaf,
            'children': {ch: node.dict() for ch, node in self.children.items()}
        }
                
    def __repr__(self) -> str:
        return f"TrieNode({self.leaf}, {self.children})"


def fetch_tlds():
    with httpx.Client() as client:
        resp = client.get("https://data.iana.org/TLD/tlds-alpha-by-domain.txt")
    resp.raise_for_status()
    return resp.text.splitlines()


def process_tlds(tlds: list[str], ascii_only: bool = False):
    tlds = [tld.strip().lower()
            for tld in tlds if tld.strip() and not tld.startswith("#")]

    ret = []
    for tld in tlds:
        if not ascii_only and tld.startswith("xn--"):
            ret.append(tld.encode().decode("idna"))
        else:
            ret.append(tld)
    ret.sort()
    return ret


TEMPLATE = """package urlx

// TLDs is generated from https://data.iana.org/TLD/tlds-alpha-by-domain.txt
var TLDs = []string{
%s
}

// TLDRegex is regex pattern to match TLDs
//
//nolint:revive // it's long
var TLDRegex = `%s`
"""


def main():
    tlds = fetch_tlds()
    tlds = process_tlds(tlds)

    root = TrieNode()
    for tld in tlds:
        root.insert(tld)
    tlds_regex = root.to_regex()
    # pprint.pprint(root.dict())
    # print(tlds_regex)

    tlds = "\n".join(f'\t"{tld}",' for tld in tlds)
    with open("util/urlx/tlds.go", "w", encoding='utf-8') as f:
        f.write(TEMPLATE % (tlds, tlds_regex))
    print("Done")


if __name__ == "__main__":
    main()
