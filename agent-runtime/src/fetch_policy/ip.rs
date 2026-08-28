use ipnet::IpNet;
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};

pub(crate) fn is_restricted(address: IpAddr, configured: &[IpNet]) -> bool {
    if configured.iter().any(|network| network.contains(&address)) {
        return true;
    }
    match address {
        IpAddr::V4(address) => is_restricted_v4(address),
        IpAddr::V6(address) => {
            embedded_v4(address)
                .into_iter()
                .any(|embedded| is_restricted(IpAddr::V4(embedded), configured))
                || is_restricted_v6_enclosing(address)
        }
    }
}

fn is_restricted_v4(address: Ipv4Addr) -> bool {
    let value = u32::from(address);
    if v4_in_prefix(value, 0xc000_0000, 24) {
        return !matches!(value, 0xc000_0009 | 0xc000_000a);
    }
    if v4_in_prefix(value, 0xc058_6300, 24) {
        return value != 0xc058_6302;
    }
    [
        (0x0000_0000, 8),
        (0x0a00_0000, 8),
        (0x6440_0000, 10),
        (0x7f00_0000, 8),
        (0xa9fe_0000, 16),
        (0xac10_0000, 12),
        (0xc000_0200, 24),
        (0xc0a8_0000, 16),
        (0xc612_0000, 15),
        (0xc633_6400, 24),
        (0xcb00_7100, 24),
        (0xe000_0000, 4),
        (0xf000_0000, 4),
    ]
    .into_iter()
    .any(|(network, prefix)| v4_in_prefix(value, network, prefix))
}

fn is_restricted_v6_enclosing(address: Ipv6Addr) -> bool {
    let value = u128::from(address);
    if in_v6_prefix(value, 0, 96)
        || in_v6_prefix(value, parse_v6("::ffff:0:0"), 96)
        || in_v6_prefix(value, parse_v6("64:ff9b:1::"), 48)
    {
        return true;
    }
    if in_v6_prefix(value, parse_v6("64:ff9b::"), 96)
        || in_v6_prefix(value, parse_v6("2002::"), 16)
        || in_v6_prefix(value, parse_v6("2001::"), 32)
    {
        return false;
    }
    if !in_v6_prefix(value, parse_v6("2000::"), 3) {
        return true;
    }
    if in_v6_prefix(value, parse_v6("2001::"), 23) {
        let globally_reachable = [
            parse_v6("2001:1::1"),
            parse_v6("2001:1::2"),
            parse_v6("2001:1::3"),
        ]
        .contains(&value)
            || in_v6_prefix(value, parse_v6("2001:3::"), 32)
            || in_v6_prefix(value, parse_v6("2001:4:112::"), 48)
            || in_v6_prefix(value, parse_v6("2001:30::"), 28);
        return !globally_reachable;
    }
    [(parse_v6("2001:db8::"), 32), (parse_v6("3fff::"), 20)]
        .into_iter()
        .any(|(network, prefix)| in_v6_prefix(value, network, prefix))
}

fn embedded_v4(address: Ipv6Addr) -> Vec<Ipv4Addr> {
    let value = u128::from(address);
    let octets = address.octets();
    if in_v6_prefix(value, 0, 96) {
        return vec![Ipv4Addr::new(
            octets[12], octets[13], octets[14], octets[15],
        )];
    }
    if in_v6_prefix(value, parse_v6("::ffff:0:0"), 96) {
        return vec![Ipv4Addr::new(
            octets[12], octets[13], octets[14], octets[15],
        )];
    }
    if in_v6_prefix(value, parse_v6("64:ff9b::"), 96) {
        return vec![Ipv4Addr::new(
            octets[12], octets[13], octets[14], octets[15],
        )];
    }
    if in_v6_prefix(value, parse_v6("64:ff9b:1::"), 48) {
        return vec![Ipv4Addr::new(octets[6], octets[7], octets[9], octets[10])];
    }
    if in_v6_prefix(value, parse_v6("2002::"), 16) {
        return vec![Ipv4Addr::new(octets[2], octets[3], octets[4], octets[5])];
    }
    if in_v6_prefix(value, parse_v6("2001::"), 32) {
        return vec![
            Ipv4Addr::new(octets[4], octets[5], octets[6], octets[7]),
            Ipv4Addr::new(!octets[12], !octets[13], !octets[14], !octets[15]),
        ];
    }
    Vec::new()
}

fn v4_in_prefix(value: u32, network: u32, prefix: u32) -> bool {
    let mask = u32::MAX << (32 - prefix);
    value & mask == network & mask
}

fn in_v6_prefix(value: u128, network: u128, prefix: u32) -> bool {
    let mask = u128::MAX << (128 - prefix);
    value & mask == network & mask
}

fn parse_v6(raw: &str) -> u128 {
    u128::from(
        raw.parse::<Ipv6Addr>()
            .expect("static IPv6 prefix must parse"),
    )
}
