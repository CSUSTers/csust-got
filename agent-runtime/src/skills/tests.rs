use super::loader::{RuntimeSkillLoadHookPoint, load_runtime_skill_descriptors_with_hook};
use super::*;
use sha2::Sha256;
use std::{fs, path::Path};
use tempfile::tempdir;

fn write_skill(root: &Path, name: &str, content: &[u8]) {
    let skill = root.join(name);
    fs::create_dir_all(&skill).unwrap();
    fs::write(skill.join("SKILL.md"), content).unwrap();
}

fn canonical_bytes_for_test(schema_version: u32, skills: &[SkillDescriptor]) -> Vec<u8> {
    let mut bytes = Vec::new();
    let mut write = |value: &str| {
        bytes.extend_from_slice(&(value.len() as u64).to_be_bytes());
        bytes.extend_from_slice(value.as_bytes());
    };
    write(&schema_version.to_string());
    write(&skills.len().to_string());
    for skill in skills {
        for value in [
            &skill.name,
            &skill.description,
            &skill.content,
            &skill.sha256,
            &skill.source,
            &skill.virtual_path,
        ] {
            write(value);
        }
    }
    bytes
}

#[test]
fn runtime_skill_snapshot_loads_only_direct_children_and_preserves_content() {
    let root = tempdir().unwrap();
    fs::write(root.path().join("README.md"), "ignored").unwrap();
    write_skill(root.path(), "alpha", b"# Alpha\nAlpha description.\n");
    fs::create_dir_all(root.path().join("alpha/scripts")).unwrap();
    fs::write(root.path().join("alpha/scripts/tool.sh"), "ignored").unwrap();
    write_skill(
        &root.path().join("alpha/nested"),
        "ignored",
        b"# Ignored\nNot discovered.\n",
    );

    let snapshot = FrozenSkillSnapshot::load(Some(root.path())).unwrap();
    assert_eq!(snapshot.snapshot().skills.len(), 1);
    assert_eq!(snapshot.snapshot().skills[0].name, "alpha");
    assert_eq!(snapshot.snapshot().skills[0].source, "runtime-global");
    assert_eq!(
        snapshot.snapshot().skills[0].virtual_path,
        "/skills/alpha/SKILL.md"
    );
    assert_eq!(
        snapshot.snapshot().skills[0].content,
        "# Alpha\nAlpha description.\n"
    );
    assert!(
        FrozenSkillSnapshot::load(None)
            .unwrap()
            .snapshot()
            .skills
            .is_empty()
    );
}

#[test]
fn runtime_skill_snapshot_rejects_symlinks_malformed_utf8_and_capacity_overflow() {
    let malformed = tempdir().unwrap();
    write_skill(malformed.path(), "alpha", b"# Alpha\n\xff");
    assert!(FrozenSkillSnapshot::load(Some(malformed.path())).is_err());

    let overflow = tempdir().unwrap();
    write_skill(
        overflow.path(),
        "alpha",
        &vec![b'a'; MAX_SKILL_FILE_BYTES + 1],
    );
    assert!(FrozenSkillSnapshot::load(Some(overflow.path())).is_err());

    let non_canonical = tempdir().unwrap();
    write_skill(
        non_canonical.path(),
        "NotCanonical",
        b"# Alpha\nDescription\n",
    );
    assert!(FrozenSkillSnapshot::load(Some(non_canonical.path())).is_err());

    let missing = tempdir().unwrap();
    fs::create_dir(missing.path().join("alpha")).unwrap();
    assert!(FrozenSkillSnapshot::load(Some(missing.path())).is_err());

    let non_regular = tempdir().unwrap();
    fs::create_dir_all(non_regular.path().join("alpha/SKILL.md")).unwrap();
    assert!(FrozenSkillSnapshot::load(Some(non_regular.path())).is_err());

    let exact_file_limit = tempdir().unwrap();
    write_skill(
        exact_file_limit.path(),
        "alpha",
        &vec![b'a'; MAX_SKILL_FILE_BYTES],
    );
    assert!(FrozenSkillSnapshot::load(Some(exact_file_limit.path())).is_ok());

    let count_overflow = tempdir().unwrap();
    for index in 0..=MAX_SKILLS_PER_SOURCE {
        write_skill(
            count_overflow.path(),
            &format!("skill-{index}"),
            b"Description\n",
        );
    }
    assert!(FrozenSkillSnapshot::load(Some(count_overflow.path())).is_err());

    let aggregate_overflow = tempdir().unwrap();
    for index in 0..17 {
        write_skill(
            aggregate_overflow.path(),
            &format!("skill-{index}"),
            &vec![b'a'; MAX_SKILL_FILE_BYTES],
        );
    }
    assert!(FrozenSkillSnapshot::load(Some(aggregate_overflow.path())).is_err());

    #[cfg(unix)]
    {
        use std::os::unix::fs::symlink;

        let fixture = tempdir().unwrap();
        let target = fixture.path().join("target");
        fs::create_dir(&target).unwrap();
        write_skill(&target, "alpha", b"# Alpha\nDescription\n");

        let root_link = fixture.path().join("root-link");
        symlink(&target, &root_link).unwrap();
        assert!(FrozenSkillSnapshot::load(Some(&root_link)).is_err());

        let child_link_root = fixture.path().join("child-link-root");
        fs::create_dir(&child_link_root).unwrap();
        symlink(target.join("alpha"), child_link_root.join("alpha")).unwrap();
        assert!(FrozenSkillSnapshot::load(Some(&child_link_root)).is_err());

        let skill_link_root = fixture.path().join("skill-link-root");
        fs::create_dir(skill_link_root.join("alpha")).unwrap();
        symlink(
            target.join("alpha/SKILL.md"),
            skill_link_root.join("alpha/SKILL.md"),
        )
        .unwrap();
        assert!(FrozenSkillSnapshot::load(Some(&skill_link_root)).is_err());
    }
}

#[test]
fn runtime_skill_description_and_hash_match_cross_language_vector() {
    let root = tempdir().unwrap();
    write_skill(root.path(), "alpha", b"# Alpha\nAlpha skill.\n");

    let snapshot = FrozenSkillSnapshot::load(Some(root.path())).unwrap();
    let skill = &snapshot.snapshot().skills[0];
    assert_eq!(skill.description, "Alpha skill.");
    assert_eq!(
        skill.sha256,
        "1fbaf47fc271ddf43f40756a9a3d2776156e7e2c6472bf9bf4cd66ea143be574"
    );
    assert_eq!(
        snapshot.snapshot().snapshot_sha256,
        "66d894d641ce04fcc04eaec3837a0dac24a27dd5ee9160ce8d3871ce0155f9ee"
    );
    let expected = format!(
        "{:x}",
        Sha256::digest(canonical_bytes_for_test(
            SKILL_SCHEMA_VERSION,
            &snapshot.snapshot().skills,
        ))
    );
    assert_eq!(snapshot.snapshot().snapshot_sha256, expected);
    assert_eq!(
        snapshot.json_bytes(),
        Bytes::from(serde_json::to_vec(snapshot.snapshot()).unwrap())
    );
    assert!(snapshot.json_bytes().len() <= MAX_SKILLS_RESPONSE_BYTES);

    let descriptions = tempdir().unwrap();
    let long_description = "你".repeat(201);
    write_skill(
        descriptions.path(),
        "beta",
        format!("  ### Heading\n\n{long_description}\n").as_bytes(),
    );
    let description = FrozenSkillSnapshot::load(Some(descriptions.path()))
        .unwrap()
        .snapshot()
        .skills[0]
        .description
        .clone();
    assert_eq!(description.chars().count(), 200);
    assert_eq!(description, "你".repeat(200));
}

#[test]
fn runtime_skill_snapshot_rejects_same_source_duplicate_descriptors() {
    let descriptor = SkillDescriptor {
        name: "alpha".to_string(),
        description: "Alpha skill.".to_string(),
        content: "# Alpha\nAlpha skill.\n".to_string(),
        sha256: "1fbaf47fc271ddf43f40756a9a3d2776156e7e2c6472bf9bf4cd66ea143be574".to_string(),
        source: "runtime-global".to_string(),
        virtual_path: "/skills/alpha/SKILL.md".to_string(),
    };

    assert!(build_snapshot_for_test(vec![descriptor.clone(), descriptor]).is_err());
}

#[test]
fn frozen_runtime_skill_snapshot_ignores_post_startup_file_changes() {
    let root = tempdir().unwrap();
    write_skill(root.path(), "alpha", b"# Alpha\nBefore startup.\n");
    let frozen = FrozenSkillSnapshot::load(Some(root.path())).unwrap();
    fs::write(
        root.path().join("alpha/SKILL.md"),
        b"# Alpha\nAfter startup.\n",
    )
    .unwrap();

    assert_eq!(
        frozen.snapshot().skills[0].content,
        "# Alpha\nBefore startup.\n"
    );
}

#[test]
fn runtime_skill_loader_rejects_root_directory_swap() {
    let fixture = tempdir().unwrap();
    let root = fixture.path().join("root");
    let attacker = fixture.path().join("attacker");
    let retired = fixture.path().join("retired-root");
    write_skill(&root, "alpha", b"# Alpha\nTrusted content.\n");
    write_skill(&attacker, "alpha", b"# Alpha\nAttacker content.\n");
    let mut swapped = false;

    let result = load_runtime_skill_descriptors_with_hook(&root, |point, _| {
        if point == RuntimeSkillLoadHookPoint::RootIdentity && !swapped {
            fs::rename(&root, &retired).unwrap();
            fs::rename(&attacker, &root).unwrap();
            swapped = true;
        }
    });

    assert!(swapped);
    assert!(result.is_err(), "loader returned swapped root content");
}

#[test]
fn runtime_skill_loader_rejects_child_directory_swap() {
    let fixture = tempdir().unwrap();
    let root = fixture.path().join("root");
    let attacker = fixture.path().join("attacker-child");
    let retired = fixture.path().join("retired-child");
    let child = root.join("alpha");
    write_skill(&root, "alpha", b"# Alpha\nTrusted content.\n");
    fs::create_dir(&attacker).unwrap();
    fs::write(attacker.join("SKILL.md"), b"# Alpha\nAttacker content.\n").unwrap();
    let mut swapped = false;

    let result = load_runtime_skill_descriptors_with_hook(&root, |point, path| {
        if point == RuntimeSkillLoadHookPoint::ChildBoundary && path == child && !swapped {
            fs::rename(&child, &retired).unwrap();
            fs::rename(&attacker, &child).unwrap();
            swapped = true;
        }
    });

    assert!(swapped);
    assert!(result.is_err(), "loader returned swapped child content");
}

#[test]
fn runtime_skill_loader_rejects_skill_file_swap() {
    let fixture = tempdir().unwrap();
    let root = fixture.path().join("root");
    let skill = root.join("alpha/SKILL.md");
    let attacker = fixture.path().join("attacker.md");
    let retired = fixture.path().join("retired.md");
    write_skill(&root, "alpha", b"# Alpha\nTrusted content.\n");
    fs::write(&attacker, b"# Alpha\nAttacker content.\n").unwrap();
    let mut swapped = false;

    let result = load_runtime_skill_descriptors_with_hook(&root, |point, path| {
        if point == RuntimeSkillLoadHookPoint::SkillHandleOpened && path == skill && !swapped {
            fs::rename(&skill, &retired).unwrap();
            fs::rename(&attacker, &skill).unwrap();
            swapped = true;
        }
    });

    assert!(swapped);
    assert!(result.is_err(), "loader returned swapped file content");
}

#[test]
fn runtime_skill_loader_rejects_skill_file_symlink_swap() {
    let fixture = tempdir().unwrap();
    let root = fixture.path().join("root");
    let skill = root.join("alpha/SKILL.md");
    let attacker = fixture.path().join("attacker.md");
    let retired = fixture.path().join("retired.md");
    write_skill(&root, "alpha", b"# Alpha\nTrusted content.\n");
    fs::write(&attacker, b"# Alpha\nAttacker content.\n").unwrap();
    let probe = fixture.path().join("symlink-probe");
    match create_file_symlink(&attacker, &probe) {
        Ok(()) => fs::remove_file(&probe).unwrap(),
        Err(error) => {
            #[cfg(windows)]
            if error.kind() == std::io::ErrorKind::PermissionDenied {
                return;
            }
            panic!("create file symlink: {error}");
        }
    }
    let mut swapped = false;

    let result = load_runtime_skill_descriptors_with_hook(&root, |point, path| {
        if point == RuntimeSkillLoadHookPoint::SkillHandleOpened && path == skill && !swapped {
            fs::rename(&skill, &retired).unwrap();
            create_file_symlink(&attacker, &skill).unwrap();
            swapped = true;
        }
    });

    assert!(swapped);
    assert!(result.is_err(), "loader returned symlink target content");
}

fn create_file_symlink(target: &Path, link: &Path) -> std::io::Result<()> {
    #[cfg(unix)]
    {
        std::os::unix::fs::symlink(target, link)
    }
    #[cfg(windows)]
    {
        std::os::windows::fs::symlink_file(target, link)
    }
    #[cfg(not(any(unix, windows)))]
    {
        let _ = (target, link);
        Err(std::io::Error::new(
            std::io::ErrorKind::Unsupported,
            "file symlinks are unsupported",
        ))
    }
}
