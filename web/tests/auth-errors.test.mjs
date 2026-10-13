import test from "node:test";
import assert from "node:assert/strict";
import { ApiError } from "../src/api.ts";
import { authErrorMessage } from "../src/lib/auth-errors.ts";

test("authentication errors have Chinese messages and preserve uniform recovery failures", () => {
  assert.equal(authErrorMessage(new ApiError(401, "invalid username, password or code")), "用户名、密码或验证码不正确");
  assert.equal(authErrorMessage(new ApiError(401, "恢复验证失败，请检查信息或稍后重试")), "恢复验证失败，请检查信息或稍后重试");
  assert.equal(authErrorMessage(new ApiError(429, "too many attempts, slow down")), "尝试次数过多，请稍后重试");
  assert.equal(authErrorMessage(new ApiError(500, "database diagnostic")), "暂时无法处理，请稍后重试");
  assert.equal(authErrorMessage(new Error("fetch failed")), "请求失败，请检查网络");
});
