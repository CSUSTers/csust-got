use crate::skills::{FrozenSkillSnapshot, is_canonical_skill_name};
use serde::{Deserialize, Deserializer, de};
use std::{
    collections::{BTreeMap, BTreeSet},
    fmt,
};

pub(crate) const BASH_ENV_VERSION: u32 = 1;
pub(crate) const MAX_ENV_BYTES: usize = 8 * 1024;
const MAX_ENV_ITEMS: usize = 64;
const MAX_SKILL_LAYERS: usize = 128;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct EnvError(pub(crate) &'static str);

impl fmt::Display for EnvError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.0)
    }
}

impl std::error::Error for EnvError {}

#[derive(Clone, Default)]
pub struct ApplicationEnv(BTreeMap<String, String>);

impl fmt::Debug for ApplicationEnv {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("ApplicationEnv([redacted])")
    }
}

impl<'de> Deserialize<'de> for ApplicationEnv {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct EnvVisitor;
        impl<'de> de::Visitor<'de> for EnvVisitor {
            type Value = ApplicationEnv;
            fn expecting(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
                f.write_str("an application environment map")
            }
            fn visit_none<E: de::Error>(self) -> Result<Self::Value, E> {
                Ok(ApplicationEnv::default())
            }
            fn visit_unit<E: de::Error>(self) -> Result<Self::Value, E> {
                self.visit_none()
            }
            fn visit_some<D: Deserializer<'de>>(self, d: D) -> Result<Self::Value, D::Error> {
                d.deserialize_map(self)
            }
            fn visit_map<M: de::MapAccess<'de>>(self, mut map: M) -> Result<Self::Value, M::Error> {
                let mut values = BTreeMap::new();
                while let Some((name, value)) = map.next_entry::<String, String>()? {
                    if values.insert(name, value).is_some() {
                        return Err(de::Error::custom("runtime_env_duplicate"));
                    }
                    if values.len() > MAX_ENV_ITEMS {
                        return Err(de::Error::custom("runtime_env_limit"));
                    }
                }
                Ok(ApplicationEnv(values))
            }
        }
        deserializer.deserialize_option(EnvVisitor)
    }
}

#[derive(Clone, Debug)]
pub enum SkillEnvLayer {
    BotLocal { name: String, env: ApplicationEnv },
    RuntimeGlobal { name: String, skill_sha256: String },
}

impl<'de> Deserialize<'de> for SkillEnvLayer {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct LayerVisitor;
        impl<'de> de::Visitor<'de> for LayerVisitor {
            type Value = SkillEnvLayer;

            fn expecting(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
                f.write_str("a skill environment layer")
            }

            fn visit_map<M: de::MapAccess<'de>>(self, mut map: M) -> Result<Self::Value, M::Error> {
                let (mut source, mut name, mut env, mut skill_sha256) = (None, None, None, None);
                while let Some(key) = map.next_key::<String>()? {
                    match key.as_str() {
                        "source" if source.is_none() => source = Some(map.next_value::<String>()?),
                        "name" if name.is_none() => name = Some(map.next_value::<String>()?),
                        "env" if env.is_none() => env = Some(map.next_value::<ApplicationEnv>()?),
                        "skill_sha256" if skill_sha256.is_none() => {
                            skill_sha256 = Some(map.next_value::<String>()?)
                        }
                        _ => return Err(de::Error::custom("runtime_env_request")),
                    }
                }
                let name = name.ok_or_else(|| de::Error::custom("runtime_env_request"))?;
                match source.as_deref() {
                    Some("bot-local") if skill_sha256.is_none() => Ok(SkillEnvLayer::BotLocal {
                        name,
                        env: env.ok_or_else(|| de::Error::custom("runtime_env_request"))?,
                    }),
                    Some("runtime-global") if env.is_none() => Ok(SkillEnvLayer::RuntimeGlobal {
                        name,
                        skill_sha256: skill_sha256
                            .ok_or_else(|| de::Error::custom("runtime_env_request"))?,
                    }),
                    _ => Err(de::Error::custom("runtime_env_request")),
                }
            }
        }
        deserializer.deserialize_map(LayerVisitor)
    }
}

pub(crate) fn deserialize_skill_layers<'de, D: Deserializer<'de>>(
    d: D,
) -> Result<Vec<SkillEnvLayer>, D::Error> {
    let layers = Option::<Vec<SkillEnvLayer>>::deserialize(d)?.unwrap_or_default();
    if layers.len() > MAX_SKILL_LAYERS {
        return Err(de::Error::custom("runtime_env_limit"));
    }
    Ok(layers)
}

pub(crate) fn validate_application_pair(name: &str, value: &str) -> Result<(), EnvError> {
    let bytes = name.as_bytes();
    if bytes.is_empty()
        || bytes.len() > 128
        || !(bytes[0].is_ascii_alphabetic() || bytes[0] == b'_')
        || !bytes
            .iter()
            .all(|b| b.is_ascii_alphanumeric() || *b == b'_')
    {
        return Err(EnvError("runtime_env_name"));
    }
    let upper = name.to_ascii_uppercase();
    let reserved = [
        "PATH",
        "HOME",
        "SHELL",
        "ENV",
        "BASH_ENV",
        "SHELLOPTS",
        "BASHOPTS",
        "IFS",
        "CDPATH",
        "PWD",
        "OLDPWD",
        "SHLVL",
        "PS4",
        "PROMPT_COMMAND",
        "GLOBIGNORE",
        "TMPDIR",
        "TMP",
        "TEMP",
        "GLIBC_TUNABLES",
        "GCONV_PATH",
        "LOCPATH",
        "NLSPATH",
        "HTTP_PROXY",
        "HTTPS_PROXY",
        "ALL_PROXY",
        "NO_PROXY",
    ];
    let prefixes = [
        "AGENT_RUNTIME_",
        "AGENT_FETCH_",
        "LD_",
        "DYLD_",
        "BASH_",
        "PROOT_",
        "MALLOC_",
        "GIT_",
        "PYTHON",
        "PERL",
        "RUBY",
        "NODE_",
        "JAVA_",
        "JDK_",
        "_JAVA_",
        "SSL_",
        "CURL_",
        "WGET_",
    ];
    if reserved.contains(&upper.as_str()) || prefixes.iter().any(|p| upper.starts_with(p)) {
        return Err(EnvError("runtime_env_reserved"));
    }
    if value.len() > 2048 || value.contains('\0') {
        return Err(EnvError("runtime_env_value"));
    }
    Ok(())
}

fn validate_pairs(pairs: &[(&String, &String)]) -> Result<(), EnvError> {
    if pairs.len() > MAX_ENV_ITEMS {
        return Err(EnvError("runtime_env_limit"));
    }
    for (name, value) in pairs {
        validate_application_pair(name, value)?;
    }
    check_json_budget(pairs)
}

fn check_json_budget<T: serde::Serialize + ?Sized>(pairs: &T) -> Result<(), EnvError> {
    if serde_json::to_vec(pairs)
        .map_err(|_| EnvError("runtime_env_limit"))?
        .len()
        > MAX_ENV_BYTES
    {
        return Err(EnvError("runtime_env_limit"));
    }
    Ok(())
}

pub(crate) fn validate_final_environment(env: &[(String, String)]) -> Result<(), EnvError> {
    let mut seen = BTreeSet::new();
    let mut app_count = 0;
    for (name, value) in env {
        if !seen.insert(name) {
            return Err(EnvError("runtime_env_duplicate"));
        }
        match name.as_str() {
            "PATH" | "HOME" => {
                if value.contains('\0') {
                    return Err(EnvError("runtime_env_value"));
                }
            }
            "AGENT_FETCH_CONTROL_FD" => {
                if value != "4" {
                    return Err(EnvError("runtime_env_reserved"));
                }
            }
            _ => {
                validate_application_pair(name, value)?;
                app_count += 1;
            }
        }
    }
    if app_count > MAX_ENV_ITEMS {
        return Err(EnvError("runtime_env_limit"));
    }
    check_json_budget(env)
}

pub(crate) fn resolve_environment(
    version: Option<u32>,
    env: &ApplicationEnv,
    layers: &[SkillEnvLayer],
    skills: &FrozenSkillSnapshot,
    mut internal: Vec<(String, String)>,
) -> Result<Vec<(String, String)>, EnvError> {
    if version.is_some_and(|v| v != BASH_ENV_VERSION)
        || ((!env.0.is_empty() || !layers.is_empty()) && version != Some(BASH_ENV_VERSION))
    {
        return Err(EnvError("runtime_env_version"));
    }
    if layers.len() > MAX_SKILL_LAYERS {
        return Err(EnvError("runtime_env_limit"));
    }
    let mut names = BTreeSet::new();
    let mut pairs = Vec::new();
    for layer in layers {
        let (name, values) = match layer {
            SkillEnvLayer::BotLocal { name, env } => (name, env),
            SkillEnvLayer::RuntimeGlobal { name, skill_sha256 } => (
                name,
                skills
                    .skill_environment(name, skill_sha256)
                    .ok_or(EnvError("runtime_env_skill"))?,
            ),
        };
        if !is_canonical_skill_name(name) {
            return Err(EnvError("runtime_env_skill"));
        }
        if !names.insert(name) {
            return Err(EnvError("runtime_env_duplicate"));
        }
        pairs.extend(values.0.iter());
        validate_pairs(&pairs)?;
    }
    pairs.extend(env.0.iter());
    validate_pairs(&pairs)?;
    let merged: BTreeMap<_, _> = pairs
        .into_iter()
        .map(|(k, v)| (k.clone(), v.clone()))
        .collect();
    internal.extend(merged);
    validate_final_environment(&internal)?;
    Ok(internal)
}

pub(crate) fn parse_dotenv(bytes: &[u8]) -> Result<ApplicationEnv, EnvError> {
    if bytes.len() > MAX_ENV_BYTES {
        return Err(EnvError("runtime_env_limit"));
    }
    let text = std::str::from_utf8(bytes).map_err(|_| EnvError("runtime_env_syntax"))?;
    if text.contains('\0') {
        return Err(EnvError("runtime_env_syntax"));
    }
    let mut values = BTreeMap::new();
    for raw in text.split('\n') {
        let line = raw.strip_suffix('\r').unwrap_or(raw);
        if line.contains('\r') {
            return Err(EnvError("runtime_env_syntax"));
        }
        let line = line.trim_matches([' ', '\t']);
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let (name, value) = line.split_once('=').ok_or(EnvError("runtime_env_syntax"))?;
        let name = name.trim_matches([' ', '\t']);
        let value = value.trim_matches([' ', '\t']);
        let value = if value.starts_with(['\'', '"']) {
            if value.len() < 2 || !value.ends_with(value.as_bytes()[0] as char) {
                return Err(EnvError("runtime_env_syntax"));
            }
            &value[1..value.len() - 1]
        } else {
            if value.ends_with(['\'', '"']) {
                return Err(EnvError("runtime_env_syntax"));
            }
            value
        };
        validate_application_pair(name, value)?;
        if values.insert(name.to_string(), value.to_string()).is_some() {
            return Err(EnvError("runtime_env_duplicate"));
        }
    }
    validate_pairs(&values.iter().collect::<Vec<_>>())?;
    Ok(ApplicationEnv(values))
}

#[cfg(test)]
mod tests;
