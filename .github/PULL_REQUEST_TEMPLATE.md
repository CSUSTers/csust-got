## 变更说明 / Summary

<!-- 这个 PR 做了什么、为什么。关联 issue 用 "Closes #123"。 -->

## 检查清单 / Checklist

- [ ] 目标分支是 `dev`（不是 `master`）
- [ ] 构建 / 格式 / 测试已通过：`make build && make fmt && make test`
- [ ] 是否新增或修改配置项 / 挂载 / 环境变量 / TZ 依赖 / Redis key 布局 / 后台任务：**是 / 否**（删掉不适用的一项）

如上一项为「是」，以下三项全部必选（见 `AGENTS.md` 的「部署文档随功能走」与 `docs/deployment_checklist.md`）：

- [ ] `docs/<feature>.md` 的「部署与配置」段已更新：键名与路径、默认值、不配置时的行为、是否需要挂载 / 环境变量 / TZ
- [ ] `config.yaml` 示例已同步更新
- [ ] 对已有部署的迁移步骤与回滚方式已写明

## 部署影响 / Deployment notes

<!-- 没有部署影响就写「无」。有则简述：运维升级到这个版本前要做什么、不做会发生什么、怎么回滚。 -->
