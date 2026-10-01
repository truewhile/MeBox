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

// 切换线路：与真实聚合源同构——把一段内嵌 HTML 交给宿主浏览器，
// 用户在页面里点选，宿主回传「操作后的页面源码」，书源再从 DOM 里
// 解析出所选线路写进源变量。（光遇聚合的 getServerSettings 即此结构）
function switchLine() {
    let hostsbk = (getVariable('云端配置') || {})['hosts'] || hosts;
    let html = '<!DOCTYPE html><html><body>'
        + '<span id="serverValue">' + BaseUrl() + '</span>'
        + '<span id="autoSwitchValue">true</span>'
        + '</body></html>';
    let body = java.startBrowserAwait(
        'data:text/html;base64,' + java.base64Encode(html), '线路设置', false
    ).body();
    let match = body.match(/id="serverValue"\s*>\s*([^<]*?)\s*<\/span>/);
    if (!match) {
        java.longToast('解析线路失败');
        return;
    }
    setVariable('线路', match[1], false);
    java.longToast('已切换到 ' + match[1]);
    return match[1];
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
