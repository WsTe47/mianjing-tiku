-- nc-interview 初始化脚本
--
-- 用 root 执行一次即可（会新建独立库与专用用户，不触碰任何已有数据库）：
--   mysql -u root -p < scripts/setup.sql
--
-- 建完后把密码写进项目根目录的 .env：
--   NC_DB_USER=nc_app
--   NC_DB_PASS=<你设的密码>
--
-- ⚠️ 请先修改下面的密码占位符再执行。
--
-- ⚠️ 密码必须满足本机 MySQL 的 validate_password 策略（Homebrew 默认 MEDIUM）：
--    长度 ≥8、含大写、含小写、含数字、含特殊字符，且不能包含用户名。
--    例如 "nc-app-2026_Xy" 可以，纯随机字母数字会被 ERROR 1819 拒绝。
--    查看当前策略：SHOW VARIABLES LIKE 'validate_password%';
--
-- 提示：出于安全考虑，DSN 里的密码建议只用这些特殊字符：% * + - = ^ _
--      （避开 @ / : ? # ! $ \ " ` 空格，免得转义或引号问题）

CREATE DATABASE IF NOT EXISTS nc_interview
    DEFAULT CHARACTER SET utf8mb4
    DEFAULT COLLATE utf8mb4_unicode_ci;

-- 专用账号：只对 nc_interview 有权限，最小授权原则
CREATE USER IF NOT EXISTS 'nc_app'@'localhost' IDENTIFIED BY 'CHANGE_ME_请改成你的密码';
CREATE USER IF NOT EXISTS 'nc_app'@'127.0.0.1' IDENTIFIED BY 'CHANGE_ME_请改成你的密码';

GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, ALTER, INDEX, REFERENCES
    ON nc_interview.* TO 'nc_app'@'localhost';
GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, ALTER, INDEX, REFERENCES
    ON nc_interview.* TO 'nc_app'@'127.0.0.1';

FLUSH PRIVILEGES;

-- 验证
SELECT '数据库已建立' AS status, SCHEMA_NAME AS db, DEFAULT_CHARACTER_SET_NAME AS charset
FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = 'nc_interview';
