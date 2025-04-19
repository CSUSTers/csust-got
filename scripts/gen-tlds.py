import encodings
from encodings import utf_8, unicode_escape
import functools
import itertools
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

    def to_regex(self, greedy=True, nocapture=True) -> str:
        xs = self._to_regex(greedy, nocapture)
        match len(xs):
            case 0:
                return ""
            case 1:
                return xs[0]
            case _:
                return '|'.join(xs)

    def _to_regex(self, greedy=True, nocapture=True) -> list[str]:
        if not self.children:
            return []
        else:
            suffix = '?' if greedy else '??'
            ret = []
            items = [item for item in self.children.items()]
            for ch, node in items:
                xs = node._to_regex(greedy, nocapture)
                match len(xs):
                    case 0:
                        follow = ""
                    case 1:
                        follow = xs[0]
                    case _:
                        follow = ('(?:' if nocapture else '(') \
                            + '|'.join(xs) + ')'
                ret.append(ch+follow)
            if self.leaf and ret:
                if len(ret) == 1 and len(ret[0]) == 1:
                    return [ret[0]+suffix]
                return [('(?:'if nocapture else '(')+f'{"|".join(ret)}){suffix}']
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


def process_tlds(tlds: list[str], punycode=False, ascii_only=False):
    tlds = [tld.strip().lower()
            for tld in tlds
            if tld.strip() and not tld.startswith("#")]
    if ascii_only:
        tlds = [tld for tld in tlds if not tld.startswith('xn--')]

    ret = []
    for tld in tlds:
        if not punycode and tld.startswith("xn--"):
            ret.append(tld.encode().decode("idna"))
        else:
            ret.append(tld)
    ret.sort(key=lambda x: (999, x) if x.startswith('xn--') else (0, x))
    return ret


TEMPLATE = """package urlx

import "slices"

// TLDsAscii is similar to [`TLDs`], but it only contains ASCII characters.
var TLDsAscii = []string{
%s
}

var punycodeTLDs = []string{
%s
}

var unicodeTLDs = []string{
%s
}

// TLDs is generated from https://data.iana.org/TLD/tlds-alpha-by-domain.txt
var TLDs = append(slices.Clone(TLDsAscii), unicodeTLDs...)

// TLDsPunycode is similar to [`TLDs`], but it convert punycode to Unicode.
var TLDsPunycode = append(slices.Clone(TLDsAscii), punycodeTLDs...)

// TLDRegex is regex pattern to match [`TLDs`]
var TLDRegex = `%s`

// TLDAsciiRegex is regex pattern to match [`TLDsAscii`]
var TLDAsciiRegex = `%s`

// TLDsPunycodeRegex is regex pattern to match [`TLDsPunycode`]
var TLDsPunycodeRegex = `%s`
"""


def main():
    tlds_raw = fetch_tlds()
    tlds = process_tlds(tlds_raw)
    tlds_punycode = process_tlds(tlds_raw, punycode=True)
    tlds_ascii = process_tlds(tlds_raw, ascii_only=True)

    tlds_trie = TrieNode()
    for tld in tlds:
        tlds_trie.insert(tld)
    tlds_regex = tlds_trie.to_regex()
    # pprint.pprint(root.dict())
    # print(tlds_regex)

    tlds_punycode_trie = TrieNode()
    for tld in tlds_punycode:
        tlds_punycode_trie.insert(tld)
    tlds_punycode_regex = tlds_punycode_trie.to_regex()

    tlds_ascii_trie = TrieNode()
    for tld in tlds_ascii:
        tlds_ascii_trie.insert(tld)
    tlds_ascii_regex = tlds_ascii_trie.to_regex()

    tlds_unicode_only = "\n".join(f'\t"{tld}",' for tld in tlds if ord(tld[0]) >= 128)
    tlds_punycode_only = "\n".join(f'\t"{tld}",' for tld in tlds_punycode if tld.startswith('xn--'))
    tlds_ascii_only = "\n".join(f'\t"{tld}",' for tld in tlds_ascii)
    with open("util/urlx/tlds.go", "w", encoding='utf-8') as f:
        f.write(TEMPLATE % (tlds_ascii_only, tlds_punycode_only, tlds_unicode_only, 
                            tlds_regex, tlds_ascii_regex, tlds_punycode_regex))
    print("Done")


if __name__ == "__main__":
    main()
