// 测试用 jsLib：模拟聚合类书源的公共函数库结构。
//
// 真实书源（如光遇聚合）的 jsLib 有十几万字符，但关键结构就是这些：
// 用 source.getVariable 做设置读写、用 cookie 存取凭证、用 BaseUrl() 拼接口地址。
// 该文件由 JSRunner 在创建时执行一次，其函数对后续所有规则 JS 可见。
// 同时保留了源码里常见的 lexical 声明（let hosts），验证跨执行可见性。

// 当前书源版本号
let localVersion = '26.9.29.1';

// 初始服务器列表
let hosts = [
    'https://v1.example-aggregate.com',
    'https://v2.example-aggregate.com'
];

// 源变量默认值
const defaultConfig = {
    线路: hosts[0],
    发现页来源: '番茄'
};

// 获取源变量
function getVariable(k) {
    if (k == undefined) k = "";
    let parsed = {};
    try {
        parsed = JSON.parse(source.getVariable());
    } catch (e) {}
    if (k == "") {
        return parsed;
    }
    let value = parsed[k];
    if (value == undefined) {
        value = defaultConfig[k];
    }
    return value != undefined ? value : "";
}

// 设置源变量
function setVariable(k, v, t) {
    if (t == undefined) t = true;
    const vs = getVariable();
    vs[k] = v;
    source.setVariable(JSON.stringify(vs, null, 4));
    if (k != '云端配置' && t) {
        java.toast('设置 ' + k + ' 为 ' + v);
    }
}

// 获取正在使用的线路
function BaseUrl() {
    let h = getVariable("线路");
    if (!h || String(h) == "undefined") {
        h = hosts[0];
    }
    return h;
}

// 获取登陆 token
function getToken() {
    let hostsbk = getVariable('云端配置') && getVariable('云端配置')['hosts'] || hosts;
    for (let i = 0; i < hostsbk.length; i++) {
        let cookieValue = String(cookie.getCookie(hostsbk[i]));
        let parts = cookieValue.split(";");
        for (let j = 0; j < parts.length; j++) {
            if (parts[j].indexOf("qttoken") != -1) {
                return parts[j].split("=")[1];
            }
        }
    }
    return "";
}

// 设置 ck
function setAllCookies(ck) {
    let hostsbk = hosts;
    for (let i = 0; i < hostsbk.length; i++) {
        cookie.setCookie(hostsbk[i], ck);
    }
}

// 移除 ck
function removeAllCookies() {
    for (let i = 0; i < hosts.length; i++) {
        cookie.removeCookie(hosts[i]);
    }
    java.toast('已退出登陆');
}

// 请求封装（自动带 token）
function request(url, method, body) {
    if (method == undefined) method = 'GET';
    if (body == undefined) body = {};
    let urla = url;
    if (url.indexOf('http') != 0) {
        urla = BaseUrl() + url;
    }
    let qttoken = getToken();
    let options = {
        method: method,
        headers: {
            'cookie': 'qttoken=' + qttoken,
            'Content-Type': 'application/json'
        },
        body: JSON.stringify(body)
    };
    return java.ajax(urla + ',' + JSON.stringify(options));
}

function parseJsonSafely(str) {
    try {
        return JSON.parse(str);
    } catch (e) {
        return null;
    }
}
