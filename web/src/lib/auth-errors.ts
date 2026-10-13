// Authentication UI never exposes raw English service/database diagnostics.
export function authErrorMessage(error: unknown): string {
  if (!(error instanceof Error) || !("status" in error) || typeof error.status !== "number") return "请求失败，请检查网络";
  if (error.status === 429) return "尝试次数过多，请稍后重试";
  if (error.status >= 500) return "暂时无法处理，请稍后重试";
  if (error.message === "account is temporarily locked after repeated failures") {
    return "验证失败次数过多，账号已暂时锁定，请 15 分钟后重试";
  }
  if (/[\u3400-\u9fff]/u.test(error.message)) return error.message;
  const messages: Record<string, string> = {
    "invalid username, password or code": "用户名、密码或验证码不正确",
    "username must be at least 3 characters": "用户名至少需要 3 个字符",
    "password must be at least 12 characters": "密码至少需要 12 个字符",
    "password must not equal the username": "密码不能与用户名相同",
    "initial setup has already been completed": "管理员已创建，请返回登录",
    "two-factor is already enabled; disable it first": "两步验证已开启，请勿重复设置",
    "no pending enrollment; start one first": "请先开始绑定两步验证",
  };
  return messages[error.message] ?? (error.status === 401 ? "身份验证失败，请检查信息或重新登录" : "操作未完成，请检查输入后重试");
}
