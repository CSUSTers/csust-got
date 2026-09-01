use bytes::Bytes;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::{fmt, path::Path, sync::Arc};

mod loader;
#[cfg(test)]
mod tests;

pub const SKILL_SCHEMA_VERSION: u32 = 1;
pub const MAX_SKILL_FILE_BYTES: usize = 64 * 1024;
pub const MAX_SKILLS_PER_SOURCE: usize = 128;
pub const MAX_SKILL_CONTENT_BYTES: usize = 1024 * 1024;
pub const MAX_SKILLS_RESPONSE_BYTES: usize = 8 * 1024 * 1024;

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct SkillDescriptor {
    pub name: String,
    pub description: String,
    pub content: String,
    pub sha256: String,
    pub source: String,
    pub virtual_path: String,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct SkillSnapshot {
    pub schema_version: u32,
    pub snapshot_sha256: String,
    pub skills: Vec<SkillDescriptor>,
}

#[derive(Clone, Debug)]
pub struct FrozenSkillSnapshot {
    snapshot: Arc<SkillSnapshot>,
    json: Bytes,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SkillSnapshotError {
    message: String,
}

impl SkillSnapshotError {
    fn new(message: impl Into<String>) -> Self {
        Self {
            message: message.into(),
        }
    }
}

impl fmt::Display for SkillSnapshotError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.message)
    }
}

impl std::error::Error for SkillSnapshotError {}

impl FrozenSkillSnapshot {
    pub fn load(root: Option<&Path>) -> Result<Self, SkillSnapshotError> {
        match root {
            Some(root) => Self::from_snapshot(build_validated_snapshot(
                loader::load_runtime_skill_descriptors(root)?,
            )?),
            None => Self::empty(),
        }
    }

    pub fn empty() -> Result<Self, SkillSnapshotError> {
        Self::from_snapshot(build_validated_snapshot(Vec::new())?)
    }

    fn from_snapshot(snapshot: SkillSnapshot) -> Result<Self, SkillSnapshotError> {
        let json = serde_json::to_vec(&snapshot).map_err(|error| {
            SkillSnapshotError::new(format!("serialize skill snapshot: {error}"))
        })?;
        if json.len() > MAX_SKILLS_RESPONSE_BYTES {
            return Err(SkillSnapshotError::new(
                "serialized skill snapshot exceeds response capacity",
            ));
        }
        Ok(Self {
            snapshot: Arc::new(snapshot),
            json: Bytes::from(json),
        })
    }

    pub fn snapshot(&self) -> &SkillSnapshot {
        &self.snapshot
    }

    pub fn json_bytes(&self) -> Bytes {
        self.json.clone()
    }
}

pub fn snapshot_sha256(schema_version: u32, sorted: &[SkillDescriptor]) -> String {
    let mut hasher = Sha256::new();
    for value in [schema_version.to_string(), sorted.len().to_string()] {
        hash_length_prefixed(&mut hasher, &value);
    }
    let mut descriptors = sorted.to_vec();
    descriptors.sort_by(|left, right| left.name.cmp(&right.name));
    for descriptor in &descriptors {
        for value in [
            &descriptor.name,
            &descriptor.description,
            &descriptor.content,
            &descriptor.sha256,
            &descriptor.source,
            &descriptor.virtual_path,
        ] {
            hash_length_prefixed(&mut hasher, value);
        }
    }
    format!("{:x}", hasher.finalize())
}

fn hash_length_prefixed(hasher: &mut Sha256, value: &str) {
    hasher.update((value.len() as u64).to_be_bytes());
    hasher.update(value.as_bytes());
}

fn build_validated_snapshot(
    mut skills: Vec<SkillDescriptor>,
) -> Result<SkillSnapshot, SkillSnapshotError> {
    if skills.len() > MAX_SKILLS_PER_SOURCE {
        return Err(SkillSnapshotError::new("skill count exceeds capacity"));
    }
    let mut aggregate_content_bytes = 0usize;
    for descriptor in &skills {
        validate_descriptor(descriptor)?;
        aggregate_content_bytes = aggregate_content_bytes
            .checked_add(descriptor.content.len())
            .ok_or_else(|| SkillSnapshotError::new("skill content size overflows capacity"))?;
        if aggregate_content_bytes > MAX_SKILL_CONTENT_BYTES {
            return Err(SkillSnapshotError::new(
                "skill content exceeds aggregate capacity",
            ));
        }
    }
    skills.sort_by(|left, right| left.name.cmp(&right.name));
    if skills.windows(2).any(|pair| pair[0].name == pair[1].name) {
        return Err(SkillSnapshotError::new(
            "duplicate runtime-global skill descriptor",
        ));
    }
    Ok(SkillSnapshot {
        schema_version: SKILL_SCHEMA_VERSION,
        snapshot_sha256: snapshot_sha256(SKILL_SCHEMA_VERSION, &skills),
        skills,
    })
}

fn validate_descriptor(descriptor: &SkillDescriptor) -> Result<(), SkillSnapshotError> {
    if !is_canonical_skill_name(&descriptor.name) {
        return Err(SkillSnapshotError::new(
            "skill descriptor name is not canonical",
        ));
    }
    if descriptor.source != "runtime-global" {
        return Err(SkillSnapshotError::new(
            "skill descriptor source must be runtime-global",
        ));
    }
    if descriptor.virtual_path != format!("/skills/{}/SKILL.md", descriptor.name) {
        return Err(SkillSnapshotError::new(
            "skill descriptor virtual path is invalid",
        ));
    }
    if descriptor.content.is_empty() || descriptor.content.len() > MAX_SKILL_FILE_BYTES {
        return Err(SkillSnapshotError::new(
            "skill descriptor content size is invalid",
        ));
    }
    let description = skill_description(&descriptor.content)
        .ok_or_else(|| SkillSnapshotError::new("skill descriptor must contain prose"))?;
    if descriptor.description != description {
        return Err(SkillSnapshotError::new(
            "skill descriptor description does not match content",
        ));
    }
    let hash = format!("{:x}", Sha256::digest(descriptor.content.as_bytes()));
    if descriptor.sha256 != hash {
        return Err(SkillSnapshotError::new(
            "skill descriptor content hash does not match",
        ));
    }
    Ok(())
}

fn is_canonical_skill_name(name: &str) -> bool {
    let bytes = name.as_bytes();
    (1..=64).contains(&bytes.len())
        && (bytes[0].is_ascii_lowercase() || bytes[0].is_ascii_digit())
        && bytes[1..]
            .iter()
            .all(|byte| byte.is_ascii_lowercase() || byte.is_ascii_digit() || *byte == b'-')
}

fn skill_description(content: &str) -> Option<String> {
    content.lines().find_map(|line| {
        let trimmed = line.trim();
        (!trimmed.is_empty() && !is_atx_heading(trimmed))
            .then(|| trimmed.chars().take(200).collect())
    })
}

fn is_atx_heading(line: &str) -> bool {
    let hash_count = line.bytes().take_while(|byte| *byte == b'#').count();
    if !(1..=6).contains(&hash_count) {
        return false;
    }
    match line[hash_count..].chars().next() {
        None => true,
        Some(character) => character.is_whitespace(),
    }
}

#[cfg(test)]
pub(super) fn build_snapshot_for_test(
    skills: Vec<SkillDescriptor>,
) -> Result<SkillSnapshot, SkillSnapshotError> {
    build_validated_snapshot(skills)
}
