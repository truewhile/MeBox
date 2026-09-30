// 测试用 loginUrl：模拟聚合类书源的登录逻辑。
//
// 约定与 legado 一致：
//   - loginUrl 本身是一段 JS，既是登录逻辑，也是 loginUi 各按钮 action 的函数库；
//   - 表单值通过作用域里的 result 对象读取；
//   - 成功与否靠抛异常区分（返回值被忽略）。

function login(flag) {
    let token = getToken();
    if (String(token).length > 10) {
        java.longToast('当前已登录，请退出登录后重新登录');
        return true;
    }
    let email = result.邮箱;
    let pwd = result.密码;
    if (!email || !pwd) {
        java.longToast('请先输入账号密码！');
        return false;
    }
    try {
        let data = request('/login_api', 'POST', {
            register_email: email,
            password: pwd
        });
        let response = parseJsonSafely(data);
        if (response && response.code == 0) {
            setAllCookies('qttoken=' + response.key);
            java.longToast('✅️登录成功');
            return true;
        }
        java.longToast('❌登录失败：' + ((response && response.msg) || '未知错误'));
        return false;
    } catch (e) {
        java.longToast('❌登录失败，服务器错误');
        return false;
    }
}

// 退出登陆
function logout() {
    removeAllCookies();
}

// 用户后台（需要浏览器打开）
function user() {
    if (String(getToken()).length < 10) {
        java.longToast('请先登陆');
        return;
    }
    java.startBrowserAwait(BaseUrl() + '/user', '用户后台');
}

// 查看信息
function checkStatus() {
    if (String(getToken()).length < 10) {
        java.longToast('请先登陆');
        return;
    }
    let res = parseJsonSafely(request('/user_api', 'POST'));
    if (!res || res.id == undefined) {
        java.toast('获取用户信息失败');
        return;
    }
    result.邮箱 = res.email;
    source.putLoginInfo(JSON.stringify(result));
    java.longToast('昵称：' + (res.nickname || '未设置'));
}
