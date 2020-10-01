import axios from "axios";

import config from "@/config/config";

import {
  AUTH_LOGIN,
  AUTH_LOGOUT,
  AUTH_SUCCESS,
  AUTH_ERROR,
} from "@/store/actions";

const state = {
  token:
    document.cookie.replace(
      /(?:(?:^|.*;\s*)info\s*=\s*([^;]*).*$)|^.*$/,
      "$1"
    ) || "",
  status: "",
  hasLoadedOnce: false,
};

const getters = {
  isAuthenticated: (state) => {
    return !!state.token;
  },
  authStatus: (state) => state.status,
};

const actions = {
  [AUTH_LOGIN]: (context, user) => {
    return new Promise((resolve, reject) => {
      context.commit(AUTH_LOGIN);
      axios
        .post(`${config.apiURL}/auth`, user)
        .then((resp) => {
          context.commit(AUTH_SUCCESS, resp);
          resolve(resp);
        })
        .catch((err) => {
          context.commit(AUTH_ERROR, err);
          reject(err);
        });
    });
  },
  [AUTH_LOGOUT]: (context) => {
    return new Promise((resolve, reject) => {
      context.commit(AUTH_LOGOUT);
      axios.post(`${config.apiURL}/logout`).catch((err) => {
        context.commit(AUTH_ERROR, err);
        reject(err);
      });
      resolve();
    });
  },
};

const mutations = {
  [AUTH_LOGIN]: (state) => {
    state.status = "loading";
  },
  [AUTH_SUCCESS]: (state, resp) => {
    state.status = "success";
    state.token = resp.data;
    state.hasLoadedOnce = true;
  },
  [AUTH_ERROR]: (state) => {
    state.status = "error";
    state.hasLoadedOnce = true;
  },
  [AUTH_LOGOUT]: (state) => {
    state.token = "";
  },
};

const auth = {
  state,
  getters,
  actions,
  mutations,
};

export default auth;
