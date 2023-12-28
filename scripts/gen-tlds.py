import encodings
import httpx

def fetch_tlds():
    with httpx.Client() as client:
        resp = client.get("https://data.iana.org/TLD/tlds-alpha-by-domain.txt")
    resp.raise_for_status()
    return resp.text.splitlines()

def process_tlds(tlds: list[str]):
    tlds = [tld.strip().lower() for tld in tlds if tld.strip() and not tld.startswith("#")]
    
    ret = []
    for tld in tlds:
        if tld.startswith("xn--"):
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

"""

def main():
    tlds = fetch_tlds()
    tlds = process_tlds(tlds)
    tlds = "\n".join(f'\t"{tld}",' for tld in tlds)
    with open("util/urlx/tlds.go", "w", encoding='utf-8') as f:
        f.write(TEMPLATE % tlds)
    print("Done")

if __name__ == "__main__":
    main()
