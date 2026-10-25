import type { Tone } from "./region";

export type AuditCategory = "account" | "machine" | "route" | "node" | "subscription" | "console" | "other";

export const AUDIT_CATEGORIES: { id: AuditCategory; label: string; tone: Tone | "muted" }[] = [
  { id: "account", label: "账号", tone: "lime" },
  { id: "machine", label: "机器", tone: "sky" },
  { id: "route", label: "链路", tone: "lilac" },
  { id: "node", label: "节点", tone: "peach" },
  { id: "subscription", label: "订阅", tone: "sun" },
  { id: "console", label: "终端", tone: "lime" },
  { id: "other", label: "其他", tone: "muted" },
];

const CATEGORY_BY_PREFIX: Record<string, AuditCategory> = {
  auth: "account",
  account: "account",
  setup: "account",
  telegram: "account",
  node: "machine",
  nodes: "machine",
  enroll: "machine",
  route: "route",
  routes: "route",
  singbox: "node",
  subscription: "subscription",
  console: "console",
};

const LABELS: Record<string, string> = {
  "auth.login": "登录",
  "auth.login_failed": "登录失败",
  "auth.logout": "退出登录",
  "auth.password_changed": "修改密码",
  "auth.password_recovered": "找回密码",
  "auth.recovery_configured": "设置密码找回",
  "auth.recovery_disabled": "关闭密码找回",
  "auth.sessions_revoked": "退出其他登录",
  "auth.totp_enabled": "开启两步验证",
  "auth.totp_disabled": "关闭两步验证",
  "auth.totp_enrollment_started": "开始绑定两步验证",
  "auth.username_changed": "修改用户名",
  "account.avatar_set": "更换头像",
  "account.avatar_clear": "移除头像",
  "setup.create_user": "创建管理员",
  "setup.confirm_totp": "初始化两步验证",
  "telegram.configure": "保存 Telegram 设置",
  "telegram.bind_requested": "生成 Telegram 绑定链接",
  "telegram.bind": "绑定 Telegram",
  "telegram.unbind": "解除 Telegram 绑定",
  "node.create": "添加机器",
  "node.update": "修改机器",
  "node.delete": "删除机器",
  "node.probe": "探测机器",
  "node.install_realm": "安装 realm",
  "nodes.reorder": "调整机器顺序",
  "enroll.ticket": "生成注册命令",
  "enroll.complete": "机器注册完成",
  "enroll.reinstall_ticket": "生成重装命令",
  "enroll.reinstall_complete": "机器重装完成",
  "route.create": "创建链路",
  "route.update": "修改链路",
  "route.delete": "删除链路",
  "route.apply": "下发链路",
  "route.stop": "停止链路",
  "route.verify": "检测链路",
  "routes.reorder": "调整链路顺序",
  "singbox.create": "创建节点",
  "singbox.update": "修改节点",
  "singbox.delete": "删除节点",
  "singbox.set_enabled": "启停节点",
  "singbox.export": "导出节点",
  "singbox.scan": "扫描外部节点",
  "singbox.adopt": "接管外部节点",
  "singbox.top_up": "增加流量",
  "singbox.reduce_quota": "减少流量",
  "singbox.reset_usage": "清零用量",
  "singbox.expiry_set": "设置到期时间",
  "subscription.create": "创建订阅",
  "subscription.create_failed": "创建订阅失败",
  "subscription.update": "修改订阅",
  "subscription.update_incomplete": "修改订阅未完成",
  "subscription.delete": "删除订阅",
  "subscription.delete_incomplete": "删除订阅未完成",
  "subscription.link_view": "查看订阅链接",
  "console.unlock": "解锁终端",
  "console.unlock_failed": "解锁终端失败",
  "console.open": "打开终端",
  "console.close": "关闭终端",
  "console.command_create": "新建快捷命令",
  "console.command_delete": "删除快捷命令",
};

export interface AuditAction {
  category: AuditCategory;
  /** 未收录的动作原样显示代码，后端新增动作时页面不会出现空白。 */
  label: string;
  failed: boolean;
}

export function describeAction(action: string): AuditAction {
  const prefix = action.split(".", 1)[0];
  return {
    category: CATEGORY_BY_PREFIX[prefix] ?? "other",
    label: LABELS[action] ?? action,
    failed: /failed|incomplete/.test(action),
  };
}
