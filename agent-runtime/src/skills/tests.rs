use super::loader::{
    RuntimeSkillLoadHookPoint, load_runtime_skill_descriptors_with_hook,
    load_runtime_skills_with_hook,
};
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
fn runtime_skill_environment_is_private_and_frozen_without_changing_public_hash() {
    let root = tempdir().unwrap();
    write_skill(root.path(), "alpha", b"# Alpha\nBefore startup.\n");
    let before = FrozenSkillSnapshot::load(Some(root.path())).unwrap();
    let public_bytes = before.json_bytes();
    let public_hash = before.snapshot().snapshot_sha256.clone();

    let env_path = root.path().join("alpha/.env");
    fs::write(&env_path, b"API_KEY=private-startup-marker\n").unwrap();
    let frozen = FrozenSkillSnapshot::load(Some(root.path())).unwrap();
    let skill = &frozen.snapshot().skills[0];
    let environment = frozen
        .skill_environment(&skill.name, &skill.sha256)
        .unwrap();
    assert_eq!(frozen.json_bytes(), public_bytes);
    assert_eq!(frozen.snapshot().snapshot_sha256, public_hash);
    assert!(!String::from_utf8_lossy(&frozen.json_bytes()).contains("private-startup-marker"));
    assert!(!format!("{frozen:?}").contains("private-startup-marker"));
    assert!(frozen.skill_environment("other", &skill.sha256).is_none());
    assert!(
        frozen
            .skill_environment(&skill.name, "wrong-hash")
            .is_none()
    );
    assert!(
        before
            .skill_environment(&skill.name, &skill.sha256)
            .is_some()
    );

    fs::write(&env_path, b"API_KEY=replaced-marker\n").unwrap();
    assert!(std::ptr::eq(
        environment,
        frozen
            .skill_environment(&skill.name, &skill.sha256)
            .unwrap()
    ));
    assert!(!format!("{frozen:?}").contains("replaced-marker"));
    let reloaded = FrozenSkillSnapshot::load(Some(root.path())).unwrap();
    assert_eq!(reloaded.json_bytes(), frozen.json_bytes());
    assert!(
        reloaded
            .skill_environment(&skill.name, &skill.sha256)
            .is_some()
    );
    let copied = frozen.clone();
    assert!(
        copied
            .skill_environment(&skill.name, &skill.sha256)
            .is_some()
    );
}

#[test]
fn runtime_skill_environment_rejects_invalid_files_without_exposing_path_or_values() {
    let root = tempdir().unwrap();
    write_skill(root.path(), "alpha", b"# Alpha\nDescription\n");
    let env_path = root.path().join("alpha/.env");

    fs::write(&env_path, b"# comment\n").unwrap();
    assert!(FrozenSkillSnapshot::load(Some(root.path())).is_ok());
    for invalid in [
        &b"API_KEY=private-invalid-marker\nAPI_KEY=duplicate\n"[..],
        &b"NO_EQUALS private-invalid-marker\n"[..],
        &b"API_KEY=\xffprivate-invalid-marker\n"[..],
        &b"API_KEY=private-invalid-marker\0\n"[..],
    ] {
        fs::write(&env_path, invalid).unwrap();
        let error = FrozenSkillSnapshot::load(Some(root.path())).unwrap_err();
        let display = error.to_string();
        assert!(!display.contains("private-invalid-marker"), "{display}");
        assert!(
            !display.contains(&root.path().display().to_string()),
            "{display}"
        );
        assert!(!format!("{error:?}").contains("private-invalid-marker"));
    }

    fs::write(&env_path, format!("#{}", "a".repeat(8191))).unwrap();
    assert!(FrozenSkillSnapshot::load(Some(root.path())).is_ok());
    fs::write(&env_path, format!("#{}", "a".repeat(8192))).unwrap();
    let error = FrozenSkillSnapshot::load(Some(root.path())).unwrap_err();
    assert!(error.to_string().contains("capacity"));
    assert!(
        !error
            .to_string()
            .contains(&root.path().display().to_string())
    );
}

#[test]
fn runtime_skill_environment_rejects_directory_and_symlink() {
    let fixture = tempdir().unwrap();
    write_skill(fixture.path(), "alpha", b"# Alpha\nDescription\n");
    let env_path = fixture.path().join("alpha/.env");
    fs::create_dir(&env_path).unwrap();
    assert!(FrozenSkillSnapshot::load(Some(fixture.path())).is_err());
    fs::remove_dir(&env_path).unwrap();

    let target = fixture.path().join("target.env");
    fs::write(&target, b"API_KEY=outside-marker\n").unwrap();
    match create_file_symlink(&target, &env_path) {
        Ok(()) => {
            let error = FrozenSkillSnapshot::load(Some(fixture.path())).unwrap_err();
            assert!(!error.to_string().contains("outside-marker"));
            assert!(
                !error
                    .to_string()
                    .contains(&fixture.path().display().to_string())
            );
        }
        Err(error) => {
            #[cfg(windows)]
            if error.kind() == std::io::ErrorKind::PermissionDenied {
                return;
            }
            panic!("create file symlink: {error}");
        }
    }
}

#[cfg(target_os = "linux")]
#[test]
fn runtime_skill_environment_rejects_fifo_without_opening_it() {
    use std::{ffi::CString, os::unix::ffi::OsStrExt as _};

    let root = tempdir().unwrap();
    write_skill(root.path(), "alpha", b"# Alpha\nDescription\n");
    let env_path = root.path().join("alpha/.env");
    let filename = CString::new(env_path.as_os_str().as_bytes()).unwrap();
    // SAFETY: `filename` is a valid NUL-terminated path and remains alive for the call.
    assert_eq!(unsafe { libc::mkfifo(filename.as_ptr(), 0o600) }, 0);
    let error = FrozenSkillSnapshot::load(Some(root.path())).unwrap_err();
    assert!(error.to_string().contains("regular file"));
    assert!(
        !error
            .to_string()
            .contains(&root.path().display().to_string())
    );
}

#[test]
fn runtime_skill_environment_rejects_replacement_between_handle_checks() {
    let fixture = tempdir().unwrap();
    let root = fixture.path().join("root");
    let env = root.join("alpha/.env");
    let attacker = fixture.path().join("attacker.env");
    let retired = fixture.path().join("retired.env");
    write_skill(&root, "alpha", b"# Alpha\nTrusted content.\n");
    fs::write(&env, b"API_KEY=trusted-marker\n").unwrap();
    fs::write(&attacker, b"API_KEY=attacker-marker\n").unwrap();
    let mut swapped = false;

    let result = load_runtime_skills_with_hook(&root, |point, path| {
        if point == RuntimeSkillLoadHookPoint::EnvHandleOpened && path == env && !swapped {
            fs::rename(&env, &retired).unwrap();
            fs::rename(&attacker, &env).unwrap();
            swapped = true;
        }
    });

    assert!(swapped);
    let error = result.unwrap_err();
    assert!(!error.to_string().contains("attacker-marker"));
    assert!(!error.to_string().contains(&root.display().to_string()));
}

#[test]
fn runtime_skill_environment_does_not_treat_disappearance_as_missing() {
    let root = tempdir().unwrap();
    write_skill(root.path(), "alpha", b"# Alpha\nDescription\n");
    let env = root.path().join("alpha/.env");
    fs::write(&env, b"API_KEY=private-marker\n").unwrap();
    let mut removed = false;

    let result = load_runtime_skills_with_hook(root.path(), |point, path| {
        if point == RuntimeSkillLoadHookPoint::EnvHandleOpened && path == env {
            fs::remove_file(&env).unwrap();
            removed = true;
        }
    });

    assert!(removed);
    let error = result.unwrap_err();
    assert!(!error.to_string().contains("private-marker"));
    assert!(
        !error
            .to_string()
            .contains(&root.path().display().to_string())
    );
}

#[cfg(unix)]
#[test]
fn runtime_skill_environment_rejects_unreadable_file_when_access_is_denied() {
    use std::os::unix::fs::PermissionsExt as _;

    let root = tempdir().unwrap();
    write_skill(root.path(), "alpha", b"# Alpha\nDescription\n");
    let env = root.path().join("alpha/.env");
    fs::write(&env, b"API_KEY=private-marker\n").unwrap();
    fs::set_permissions(&env, fs::Permissions::from_mode(0)).unwrap();
    if fs::File::open(&env).is_err() {
        let error = FrozenSkillSnapshot::load(Some(root.path())).unwrap_err();
        assert!(!error.to_string().contains("private-marker"));
        assert!(
            !error
                .to_string()
                .contains(&root.path().display().to_string())
        );
    }
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
